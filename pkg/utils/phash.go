package utils

import (
	"math"
	"strconv"

	"github.com/corona10/goimagehash"
	"github.com/stashapp/stash/pkg/sliceutil"
)

type Phash struct {
	SceneID int   `db:"id"`
	Hash    int64 `db:"phash"`
	// FileID is the file the phash and duration came from. The phash belongs to the FILE,
	// not the scene, so two scenes over one file have an IDENTICAL hash and identical
	// duration -- and without this field they become each other's nearest neighbour, i.e.
	// every scene of a split file is reported as a duplicate of every other one.
	// docs/ISSUE-3530-spec.md section 3; zero means "unknown" and disables the check, which
	// keeps every existing caller (and every test) behaving as before.
	FileID    int     `db:"file_id"`
	Duration  float64 `db:"duration"`
	Neighbors []int
	Bucket    int
}

func FindDuplicates(hashes []*Phash, distance int, durationDiff float64) [][]int {
	for i, scene := range hashes {
		sceneHash := goimagehash.NewImageHash(uint64(scene.Hash), goimagehash.PHash)
		for j, neighbor := range hashes {
			// Same scene, or two scenes over the SAME FILE: not a duplicate pair. The phash
			// is the file's, so the distance is 0 by construction and every segment of a
			// split file would otherwise be flagged. Skipping on FileID rather than on
			// "distance == 0" is deliberate: a distance of 0 is ALSO what two byte-identical
			// files produce, and those ARE duplicates.
			if i != j && scene.SceneID != neighbor.SceneID &&
				!(scene.FileID != 0 && scene.FileID == neighbor.FileID) {
				neighbourDurationDistance := 0.
				if scene.Duration > 0 && neighbor.Duration > 0 {
					neighbourDurationDistance = math.Abs(scene.Duration - neighbor.Duration)
				}
				if (neighbourDurationDistance <= durationDiff) || (durationDiff < 0) {
					neighborHash := goimagehash.NewImageHash(uint64(neighbor.Hash), goimagehash.PHash)
					neighborDistance, _ := sceneHash.Distance(neighborHash)
					if neighborDistance <= distance {
						scene.Neighbors = append(scene.Neighbors, j)
					}
				}
			}
		}
	}

	var buckets [][]int
	for _, scene := range hashes {
		if len(scene.Neighbors) > 0 && scene.Bucket == -1 {
			bucket := len(buckets)
			scenes := []int{scene.SceneID}
			scene.Bucket = bucket
			findNeighbors(bucket, scene.Neighbors, hashes, &scenes)

			if len(scenes) > 1 {
				buckets = append(buckets, scenes)
			}
		}
	}

	return buckets
}

func findNeighbors(bucket int, neighbors []int, hashes []*Phash, scenes *[]int) {
	for _, id := range neighbors {
		hash := hashes[id]
		if hash.Bucket == -1 {
			hash.Bucket = bucket
			*scenes = sliceutil.AppendUnique(*scenes, hash.SceneID)
			findNeighbors(bucket, hash.Neighbors, hashes, scenes)
		}
	}
}

func PhashToString(phash int64) string {
	return strconv.FormatUint(uint64(phash), 16)
}

func StringToPhash(s string) (int64, error) {
	ret, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, err
	}

	return int64(ret), nil
}
