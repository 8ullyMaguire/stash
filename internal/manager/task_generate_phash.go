package manager

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/pkg/hash/videophash"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/utils"
)

type GeneratePhashTask struct {
	repository          models.Repository
	File                *models.VideoFile
	Overwrite           bool
	fileNamingAlgorithm models.HashAlgorithm
}

func (t *GeneratePhashTask) GetDescription() string {
	return fmt.Sprintf("Generating phash for %s", t.File.Path)
}

func (t *GeneratePhashTask) Start(ctx context.Context) {
	if !t.required() {
		return
	}

	var hash int64
	set := false

	// #4393 - if there is a file with the same oshash, we can use the same phash
	// only use this if we're not overwriting
	if !t.Overwrite {
		existing, err := t.findExistingPhash(ctx)
		if err != nil {
			logger.Warnf("Error finding existing phash: %v", err)
		} else if existing != nil {
			logger.Infof("Using existing phash for %s", t.File.Path)
			hash = existing.(int64)
			set = true
		}
	}

	if !set {
		generated, err := videophash.Generate(instance.FFMpeg, t.File)
		if err != nil {
			logger.Errorf("Error generating phash for %q: %v", t.File.Path, err)
			logErrorOutput(err)
			return
		}

		// #2149 - refuse to store a phash that matches one of the known-bad
		// values. A file ffmpeg cannot decode does not fail loudly: the
		// screenshots come back as a black frame or a colour bar, and the
		// hash of that is well-formed and IDENTICAL for every file that
		// fails the same way. Storing it would upload a recurring hash to
		// StashDB, where it matches every other broken file and produces a
		// confidently wrong scene match -- the one failure a user is least
		// likely to notice.
		//
		// The storable value is assigned to the outer hash rather than
		// shadowed: a `:=` here would declare a new hash that the write below
		// never reads, and the file would be written with the zero value.
		stored, ok := storablePhash(generated)
		if !ok {
			return
		}
		hash = stored
	} else {
		// #2149 - the reuse path above copies a phash from a sibling file
		// that has the same oshash. Validating only freshly generated hashes
		// leaves this hole open: a library scanned before this check existed
		// already contains the bad values, and reusing one propagates it to
		// every other copy of that file. The file is left without a phash
		// rather than given a bad one, and the sibling keeps its own -- this
		// refuses the COPY, it does not go back and delete the original,
		// which is a separate migration concern.
		if bad, _ := utils.IsBadPhash(uint64(hash)); bad {
			logger.Warnf(
				"Not reusing phash for %q: the value %s on a file with the "+
					"same oshash is a known-bad phash (#2149). Regenerate it "+
					"to fix.", t.File.Path, utils.PhashToString(hash),
			)
			return
		}
	}

	r := t.repository
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		t.File.Fingerprints = t.File.Fingerprints.AppendUnique(models.Fingerprint{
			Type:        models.FingerprintTypePhash,
			Fingerprint: hash,
		})

		return r.File.Update(ctx, t.File)
	}); err != nil && ctx.Err() == nil {
		logger.Errorf("Error setting phash: %v", err)
	}
}

// storablePhash decides whether a freshly generated phash may be written to the
// database, and returns the value to write. #2149.
//
// It is a separate function so the DECISION is testable without ffmpeg, which
// is what makes it worth separating: the bug this prevents is not a crash, it
// is a confidently wrong value being stored and uploaded, and the only way to
// pin that is to exercise the branch directly.
//
// On rejection it logs and returns ok=false. The caller then returns without
// writing, leaving the file with no phash. That is deliberate: the file may be
// fine and the heuristic may be wrong, so this is a warning and not an error.
// No hash is a recoverable state; a wrong hash that has been uploaded to
// StashDB and matched against is not.
func storablePhash(generated *uint64) (int64, bool) {
	if generated == nil {
		return 0, false
	}

	if bad, closest := utils.IsBadPhash(*generated); bad {
		logger.Warnf(
			"Not setting phash: generated value %s is %d bits from the "+
				"known-bad phash %s, which ffmpeg produces for files it cannot "+
				"decode. The file is left without a phash (#2149).",
			utils.PhashToString(int64(*generated)),
			utils.PhashDistanceTo(*generated, closest), closest,
		)
		return 0, false
	}

	return int64(*generated), true
}

func (t *GeneratePhashTask) findExistingPhash(ctx context.Context) (interface{}, error) {
	r := t.repository
	var ret interface{}
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		oshash := t.File.Fingerprints.Get(models.FingerprintTypeOshash)

		// find other files with the same oshash
		files, err := r.File.FindByFingerprint(ctx, models.Fingerprint{
			Type:        models.FingerprintTypeOshash,
			Fingerprint: oshash,
		})
		if err != nil {
			return fmt.Errorf("finding files by oshash: %w", err)
		}

		// find the first file with a phash
		for _, file := range files {
			if phash := file.Base().Fingerprints.Get(models.FingerprintTypePhash); phash != nil {
				ret = phash
				return nil
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (t *GeneratePhashTask) required() bool {
	if t.Overwrite {
		return true
	}

	return t.File.Fingerprints.Get(models.FingerprintTypePhash) == nil
}
