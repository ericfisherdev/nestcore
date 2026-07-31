package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ericfisherdev/nestcore/media/domain"
)

// ReapableSource is the narrow, class-scoped read surface a ReaperService
// needs from a registered domain.PhotoClass's own repository: which refs it
// still references, and whether one specific ref is still referenced.
// domain.PhotoRepository satisfies this structurally (no adapter needs to
// declare the assertion), and so does any other application-owned
// repository sharing the same two methods — e.g. a chore-proof-specific
// repository carrying its own extra retention methods this interface
// deliberately does not require (ISP): retention (deciding when to delete a
// ROW) is an application policy, not shared media plumbing, and stays
// entirely on the caller's side of Run.
type ReapableSource interface {
	ListAllStorageRefs(ctx context.Context, backend domain.StorageBackend) ([]domain.StorageRef, error)
	ExistsByStorageRef(ctx context.Context, ref domain.StorageRef, backend domain.StorageBackend) (bool, error)
}

// ReaperService reclaims orphaned PhotoStore objects for an object-store
// backend: objects a repository's rows-only Delete leaves behind once
// nothing references them. It is generic over sources — one ReapableSource
// per domain.PhotoClass the caller wants swept — so a new class (a new
// consumer's own registered PhotoClass) is reaped by adding an entry to the
// map passed to NewReaperService, never by editing this package.
//
// Never invoked automatically: reaping is destructive (it permanently
// deletes object-store bytes), so wiring an unattended timer is a
// caller-side decision this package does not make for it.
//
// One pass per Run call: list every stored object of each registered
// class (lister.ListObjects), compute the set of StorageRefs still
// referenced by ANY source in sources (across every class, not just the
// class being swept — a row from one class's source can legitimately
// reference an object filed under a DIFFERENT class's own key prefix), and
// delete any object that is BOTH unreferenced AND older than graceWindow —
// an object younger than the grace window might be mid-upload (Put has
// written bytes but Create has not yet committed the referencing row), not
// genuinely orphaned; see domain.ObjectInfo.LastModified's doc.
//
// This design makes "restore a week-old DB backup against the same bucket"
// safe by construction: a restored row's StorageRef reappears in the
// referenced-set computation on the very next Run, so an object the reaper
// has not yet reached is protected again before it is ever deleted.
//
// TOCTOU note (deletion race): the referenced-set snapshot and the object
// listing are each taken once, up front, but a row can commit at any point
// after that snapshot. To narrow that window, Run re-checks EACH candidate
// individually, via a targeted ExistsByStorageRef query against every
// source, in the SAME loop iteration immediately before deleting it — this
// closes the gap down to the single instant between that final check and
// the delete call itself. That residual instant is NOT closed by this
// design; Run is intended to be invoked by an operator, never
// automatically, and MUST NOT be run concurrently with a database restore.
type ReaperService struct {
	lister      domain.ObjectLister
	store       domain.PhotoStore
	backend     domain.StorageBackend
	sources     map[domain.PhotoClass]ReapableSource
	classes     []domain.PhotoClass // sorted by name, for deterministic sweep order
	graceWindow time.Duration
}

// NewReaperService constructs a ReaperService bound to backend — the
// StorageBackend lister/store actually sweep — over sources, one
// ReapableSource per domain.PhotoClass to walk. Returns an error instead of
// panicking on a nil dependency, an invalid backend, an empty or
// invalid-keyed sources map, or a non-positive graceWindow.
func NewReaperService(
	lister domain.ObjectLister,
	store domain.PhotoStore,
	backend domain.StorageBackend,
	sources map[domain.PhotoClass]ReapableSource,
	graceWindow time.Duration,
) (*ReaperService, error) {
	switch {
	case lister == nil:
		return nil, errors.New("media/app: NewReaperService requires a non-nil ObjectLister")
	case store == nil:
		return nil, errors.New("media/app: NewReaperService requires a non-nil PhotoStore")
	case !backend.Valid():
		return nil, fmt.Errorf("media/app: NewReaperService requires a valid StorageBackend, got %q", backend)
	case len(sources) == 0:
		return nil, errors.New("media/app: NewReaperService requires at least one PhotoClass source")
	case graceWindow <= 0:
		return nil, fmt.Errorf("media/app: NewReaperService requires a positive graceWindow, got %v", graceWindow)
	}
	classes := make([]domain.PhotoClass, 0, len(sources))
	for class, source := range sources {
		if !class.Valid() {
			return nil, fmt.Errorf("media/app: NewReaperService received an unregistered PhotoClass %q", class)
		}
		if source == nil {
			return nil, fmt.Errorf("media/app: NewReaperService received a nil source for class %q", class)
		}
		classes = append(classes, class)
	}
	sort.Slice(classes, func(i, j int) bool { return classes[i].String() < classes[j].String() })

	copied := make(map[domain.PhotoClass]ReapableSource, len(sources))
	for class, source := range sources {
		copied[class] = source
	}
	return &ReaperService{lister: lister, store: store, backend: backend, sources: copied, classes: classes, graceWindow: graceWindow}, nil
}

// ReaperResult summarizes one Run: how many orphaned objects the sweep
// deleted per class.
type ReaperResult struct {
	OrphansDeleted map[domain.PhotoClass]int
}

// DryRunResult summarizes one DryRun: exactly which orphaned objects each
// class's sweep WOULD delete — refs, not just a count — so an operator can
// inspect them before ever running the destructive Run.
type DryRunResult struct {
	OrphansWouldDelete map[domain.PhotoClass][]domain.StorageRef
}

// Run executes one reaper pass (see the type doc for the full order and the
// restore-safety argument) against now, and returns a summary. now is
// caller-supplied so a test can pin it rather than depending on the wall
// clock. A caller with its own application-level retention policy (e.g.
// deleting rows older than some cutoff) must run that pass BEFORE calling
// Run, so the rows it removes have already stopped protecting their
// objects by the time Run's referenced-set snapshot is taken.
func (r *ReaperService) Run(ctx context.Context, now time.Time) (ReaperResult, error) {
	result := ReaperResult{OrphansDeleted: make(map[domain.PhotoClass]int, len(r.classes))}

	referenced, err := r.referencedRefs(ctx)
	if err != nil {
		return ReaperResult{}, fmt.Errorf("media/app: list referenced refs: %w", err)
	}

	cutoff := now.Add(-r.graceWindow)
	for _, class := range r.classes {
		candidates, err := r.orphanCandidates(ctx, class, cutoff, referenced)
		if err != nil {
			return ReaperResult{}, err
		}
		deleted := 0
		for _, ref := range candidates {
			// Targeted recheck, IMMEDIATELY before deleting: see the type
			// doc's TOCTOU note.
			stillReferenced, err := r.existsByStorageRef(ctx, ref)
			if err != nil {
				return ReaperResult{}, fmt.Errorf("media/app: recheck object %s before delete: %w", ref, err)
			}
			if stillReferenced {
				continue
			}
			if err := r.store.Delete(ctx, ref); err != nil {
				return ReaperResult{}, fmt.Errorf("media/app: delete orphaned object %s: %w", ref, err)
			}
			deleted++
		}
		result.OrphansDeleted[class] = deleted
	}
	return result, nil
}

// DryRun previews exactly what the next Run call would delete — each
// class's orphan sweep's object refs — WITHOUT deleting anything. It is
// driven by the identical candidate-selection logic Run itself uses, so its
// output is a faithful preview of Run's next call, provided no row commits
// or object ages past the grace window in between (the type doc's TOCTOU
// note applies here too, as a staleness risk on the preview rather than a
// deletion race). Unlike Run, DryRun does not perform Run's per-candidate
// existsByStorageRef recheck: that recheck exists specifically to catch a
// row committing in the gap between the bulk snapshot and an ACTUAL delete
// call, and DryRun never deletes anything, so there is no such gap to
// protect.
func (r *ReaperService) DryRun(ctx context.Context, now time.Time) (DryRunResult, error) {
	result := DryRunResult{OrphansWouldDelete: make(map[domain.PhotoClass][]domain.StorageRef, len(r.classes))}

	referenced, err := r.referencedRefs(ctx)
	if err != nil {
		return DryRunResult{}, fmt.Errorf("media/app: list referenced refs: %w", err)
	}

	cutoff := now.Add(-r.graceWindow)
	for _, class := range r.classes {
		candidates, err := r.orphanCandidates(ctx, class, cutoff, referenced)
		if err != nil {
			return DryRunResult{}, err
		}
		result.OrphansWouldDelete[class] = candidates
	}
	return result, nil
}

// orphanCandidates computes class's orphaned-object CANDIDATES from the
// BULK snapshot only — every stored object not referenced by any source in
// r.sources, and older than cutoff — WITHOUT any per-object recheck. This
// bulk list is deliberately not the final word on any individual candidate
// (see the type doc's TOCTOU note): Run performs its own recheck
// immediately before each Delete; DryRun performs no recheck at all.
func (r *ReaperService) orphanCandidates(ctx context.Context, class domain.PhotoClass, cutoff time.Time, referenced map[domain.StorageRef]struct{}) ([]domain.StorageRef, error) {
	objects, err := r.lister.ListObjects(ctx, class)
	if err != nil {
		return nil, fmt.Errorf("media/app: list stored objects for class %s: %w", class, err)
	}

	candidates := make([]domain.StorageRef, 0)
	for _, obj := range objects {
		if _, ok := referenced[obj.Key]; ok {
			continue
		}
		if obj.LastModified.After(cutoff) {
			continue
		}
		candidates = append(candidates, obj.Key)
	}
	return candidates, nil
}

// referencedRefs builds the set of StorageRefs ANY source in r.sources
// still references, across every registered class — deliberately NOT
// scoped to whichever source "owns" a given class: a cross-prefix row can
// reference an object filed under a DIFFERENT class's own prefix than the
// source it is persisted in, so checking only the nominally-matching source
// would let that reference go unseen. Computed ONCE per Run/DryRun call —
// this is the bulk CANDIDATE-selection snapshot orphanCandidates filters
// against; see existsByStorageRef for the per-object recheck that has the
// final say.
func (r *ReaperService) referencedRefs(ctx context.Context) (map[domain.StorageRef]struct{}, error) {
	set := make(map[domain.StorageRef]struct{})
	for _, class := range r.classes {
		refs, err := r.sources[class].ListAllStorageRefs(ctx, r.backend)
		if err != nil {
			return nil, fmt.Errorf("list refs for class %s: %w", class, err)
		}
		for _, ref := range refs {
			set[ref] = struct{}{}
		}
	}
	return set, nil
}

// existsByStorageRef is referencedRefs' single-ref counterpart: a fresh,
// targeted query against EVERY source — not just whichever one nominally
// "owns" the class an object was listed under — run immediately before
// Run's loop would otherwise delete ref's object. See the type doc's TOCTOU
// note for why this, not the bulk snapshot, is authoritative at delete
// time.
func (r *ReaperService) existsByStorageRef(ctx context.Context, ref domain.StorageRef) (bool, error) {
	for _, class := range r.classes {
		exists, err := r.sources[class].ExistsByStorageRef(ctx, ref, r.backend)
		if err != nil {
			return false, fmt.Errorf("check reference for class %s: %w", class, err)
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}
