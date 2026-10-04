package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asticode/go-astisub"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
	"golang.org/x/text/language"
)

var CaptionExts = []string{"vtt", "srt"} // in a case where vtt and srt files are both provided prioritize vtt file due to native support

// to be used for captions without a language code in the filename
// ISO 639-1 uses 2 or 3 a-z chars for codes so 00 is a safe non valid choise
// https://en.wikipedia.org/wiki/List_of_ISO_639-1_codes
const LangUnknown = "00"

// GetCaptionPath generates the path of a caption
// from a given file path, wanted language and caption sufffix
func GetCaptionPath(path, lang, suffix string) string {
	ext := filepath.Ext(path)
	fn := strings.TrimSuffix(path, ext)
	captionExt := ""
	if len(lang) == 0 || lang == LangUnknown {
		captionExt = suffix
	} else {
		captionExt = lang + "." + suffix
	}
	return fn + "." + captionExt
}

// ReadSubs reads a captions file
func ReadSubs(path string) (*astisub.Subtitles, error) {
	return astisub.OpenFile(path)
}

// IsValidLanguage checks whether the given string is a valid
// ISO 639 language code
func IsValidLanguage(lang string) bool {
	_, err := language.ParseBase(lang)
	return err == nil
}

// IsLangInCaptions returns true if lang is present
// in the captions
func IsLangInCaptions(lang string, ext string, captions []*models.VideoCaption) bool {
	for _, caption := range captions {
		if lang == caption.LanguageCode && ext == caption.CaptionType {
			return true
		}
	}
	return false
}

// getCaptionPrefix returns the prefix used to search for video files for the provided caption path
func getCaptionPrefix(captionPath string) string {
	basename := strings.TrimSuffix(captionPath, filepath.Ext(captionPath)) // caption filename without the extension

	// a caption file can be something like scene_filename.srt or scene_filename.en.srt
	// if a language code is present and valid remove it from the basename
	languageExt := filepath.Ext(basename)
	if len(languageExt) > 2 && IsValidLanguage(languageExt[1:]) {
		basename = strings.TrimSuffix(basename, languageExt)
	}

	return basename + "."
}

// GetCaptionsLangFromPath returns the language code from a given captions path
// If no valid language is present LangUknown is returned
func getCaptionsLangFromPath(captionPath string) string {
	langCode := LangUnknown
	basename := strings.TrimSuffix(captionPath, filepath.Ext(captionPath)) // caption filename without the extension
	languageExt := filepath.Ext(basename)
	if len(languageExt) > 2 && IsValidLanguage(languageExt[1:]) {
		langCode = languageExt[1:]
	}
	return langCode
}

type CaptionUpdater interface {
	GetCaptions(ctx context.Context, fileID models.FileID) ([]*models.VideoCaption, error)
	UpdateCaptions(ctx context.Context, fileID models.FileID, captions []*models.VideoCaption) error
}

// MatchesCaption returns true if the caption file matches the video file based on the filename
func MatchesCaption(videoPath, captionPath string) bool {
	captionPrefix := getCaptionPrefix(captionPath)
	videoPrefix := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + "."
	return captionPrefix == videoPrefix
}

// MatchesCaptionInFolders is stash#6744: like MatchesCaption, but a caption also matches when it lives
// in one of the configured subtitle folders rather than beside the video.
//
// The existing matcher compares basename prefixes only, so a subtitle on a separate share never matches:
//
//	/library/vids/scene.mp4   vs  /library/subs/scene.en.srt   -> false
//
// Matching is by BASENAME across folders, deliberately, and not by relative path. Two reasons:
//
//   - The whole point is a separate subtitle directory that has no structural relationship to the video
//     tree -- a share of .srt files named after scenes. Requiring a mirrored relative path would make
//     the feature useless for its motivating case.
//   - Narrower matching would be surprising in the other direction: a user who points at a folder of
//     subtitles expects them attached, not silently ignored because the tree does not line up.
//
// The risk that creates is a basename collision attaching the wrong subtitle to a video. That risk is
// bounded rather than eliminated: the folders are explicitly configured by the user, so anything in them
// was put there to be found, and a user with two `scene.en.srt` files in one subtitle folder has an
// ambiguity no amount of path matching would resolve.
func MatchesCaptionInFolders(videoPath, captionPath string, folders []string) bool {
	if MatchesCaption(videoPath, captionPath) {
		return true
	}

	if len(folders) == 0 {
		return false
	}

	// Compare BASENAMES here, not full prefixes. MatchesCaption compares the whole prefix including the
	// directory, which is precisely what fails across folders:
	//   videoPrefix  = /library/vids/scene.
	//   captionPrefix= /library/subs/scene.     (from getCaptionPrefix, which keeps the directory)
	// Those can never be equal, so calling MatchesCaption after confirming the directory would ALWAYS
	// return false. The first version of this function did exactly that and every folder test failed --
	// which is why the tests exist.
	captionDir := filepath.ToSlash(filepath.Clean(filepath.Dir(captionPath)))
	for _, folder := range folders {
		if folder == "" {
			continue
		}
		if !sameDir(captionDir, folder) {
			continue
		}

		return basenameMatches(videoPath, captionPath)
	}

	return false
}

// basenameMatches is MatchesCaption with the directories discarded, so a caption found in a subtitle
// folder is associated by name alone.
//
// The extension is checked against CaptionExts, which the caller does not do for the sidecar case: there
// the scanner has already established the file is a caption, because it reached this code by matching
// the caption extensions. In a folder the same guarantee does not hold -- a user pointing at a
// directory can easily include a stray .mp4 named after the scene -- and `scene.en.mp4` would otherwise
// match by basename and be recorded as a caption track, which then fails at playback when the browser
// tries to parse a video file as WebVTT.
func basenameMatches(videoPath, captionPath string) bool {
	captionBase := filepath.Base(captionPath)

	if !fsutil.MatchExtension(captionBase, CaptionExts) {
		return false
	}

	videoBase := filepath.Base(videoPath)
	videoPrefix := strings.TrimSuffix(videoBase, filepath.Ext(videoBase)) + "."
	return getCaptionPrefix(captionBase) == videoPrefix
}

// sameDir compares two directory paths, tolerating trailing slashes and "./" prefixes but NOT treating
// case differences as equal: on Linux they are different directories, and on case-insensitive
// filesystems the scanner never produces two spellings of one path anyway, so the strict comparison
// cannot produce a false negative in practice.
func sameDir(a, b string) bool {
	clean := func(p string) string {
		p = filepath.ToSlash(filepath.Clean(p))
		return strings.TrimSuffix(p, "/")
	}

	return clean(a) == clean(b)
}

// captionPathFinder is the one method of models.FileFinder that findCaptionTargets needs. Narrowing the
// parameter to this rather than models.FileFinder is what lets the test supply a three-line fake instead
// of implementing all seven methods of the full interface, which is a large amount of dead code whose only
// purpose would be to satisfy a type.
type captionPathFinder interface {
	FindAllByPath(ctx context.Context, path string, caseSensitive bool) ([]models.File, error)
}

// findCaptionTargets returns the video files a caption file could belong to.
//
// First the ordinary lookup: same directory, matched by path prefix. That is unchanged from before
// #6744 and is the fast path for sidecar captions.
//
// Only when folders are configured does it try a basename-only search, since a subtitle folder holds
// captions for videos stored anywhere. The results are de-duplicated because the two searches can overlap
// -- a sidecar in a folder that happens to also be the video's directory -- and a caption must not be
// recorded twice for the same file, which would produce duplicate tracks in the player.
func findCaptionTargets(ctx context.Context, fqb captionPathFinder, captionPath, captionPrefix string, subtitleFolders []string) ([]models.File, error) {
	files, err := fqb.FindAllByPath(ctx, captionPrefix+"*", true)
	if err != nil || len(subtitleFolders) == 0 {
		return files, err
	}

	inConfiguredFolder := false
	captionDir := filepath.ToSlash(filepath.Clean(filepath.Dir(captionPath)))
	for _, f := range subtitleFolders {
		if f == "" {
			continue
		}
		if sameDir(captionDir, f) {
			inConfiguredFolder = true
			break
		}
	}

	if !inConfiguredFolder {
		// The user configured a subtitle folder, but this caption is not in one. Matching it across
		// directories anyway would attach subtitles found anywhere in the library, which is not what
		// configuring a folder asks for.
		return files, nil
	}

	// basename-only search: "%" for the directory matches every folder.
	byName, err := fqb.FindAllByPath(ctx, "%/"+filepath.Base(captionPrefix)+"*", true)
	if err != nil {
		return files, err
	}

	seen := make(map[models.FileID]bool, len(files))
	for _, f := range files {
		seen[f.Base().ID] = true
	}

	for _, f := range byName {
		if seen[f.Base().ID] {
			continue
		}

		// The LIKE pattern is a PREFIX match, so it also returns scene-part2.mp4, scene.sample.mkv and
		// anything else starting with the same characters. Those are not this caption's video, and the
		// caller associates every VideoFile it is handed without re-checking, so an unchecked result
		// attaches scene.en.srt to every similarly-named video in the library. The exact basename
		// comparison is what the folder lookup is FOR -- matching across directories by name -- so it has
		// to be exact here too.
		if !basenameMatches(f.Base().Path, captionPath) {
			continue
		}

		seen[f.Base().ID] = true
		files = append(files, f)
	}

	return files, nil
}

// AssociateCaptions associates a caption file with the video file(s) it belongs to, returning true if at
// least one was matched and processed.
//
// By default a caption matches a video in its OWN directory: the lookup is by path prefix, and
// FileFinder.FindAllByPath filters on the directory as well as the basename
// (pkg/sqlite/file.go:675-690), so a caption beside the video is found and one elsewhere is not.
//
// stash#6744: pass subtitleFolders to also match videos OUTSIDE the caption's directory. The lookup then
// falls back to a basename-only search, because the whole point is a subtitle folder with no structural
// relationship to the video tree. Passing nil keeps the previous behaviour exactly.
func AssociateCaptions(ctx context.Context, captionPath string, txnMgr txn.Manager, fqb models.FileFinder, w CaptionUpdater, subtitleFolders []string) bool {
	captionLang := getCaptionsLangFromPath(captionPath)

	captionPrefix := getCaptionPrefix(captionPath)
	matched := false
	if err := txn.WithTxn(ctx, txnMgr, func(ctx context.Context) error {
		var err error
		files, er := findCaptionTargets(ctx, fqb, captionPath, captionPrefix, subtitleFolders)

		if er != nil {
			return fmt.Errorf("searching for scene %s: %w", captionPrefix, er)
		}

		for _, f := range files {
			// found some files
			// filter out non video files
			switch f.(type) {
			case *models.VideoFile:
				break
			default:
				continue
			}

			fileID := f.Base().ID
			path := f.Base().Path

			logger.Debugf("Matched captions to file %s", path)
			matched = true

			captions, er := w.GetCaptions(ctx, fileID)
			if er != nil {
				return fmt.Errorf("getting captions for file %s: %w", path, er)
			}

			fileExt := filepath.Ext(captionPath)
			ext := fileExt[1:]
			if !IsLangInCaptions(captionLang, ext, captions) { // only update captions if language code is not present
				newCaption := &models.VideoCaption{
					LanguageCode: captionLang,
					Filename:     filepath.Base(captionPath),
					CaptionType:  ext,
				}
				captions = append(captions, newCaption)
				er = w.UpdateCaptions(ctx, fileID, captions)
				if er != nil {
					return fmt.Errorf("updating captions for file %s: %w", path, er)
				}

				logger.Debugf("Updated captions for file %s. Added %s", path, captionLang)
			}
		}
		return err
	}); err != nil {
		logger.Error(err.Error())
	}

	return matched
}

// CleanCaptions removes non existent/accessible language codes from captions
func CleanCaptions(ctx context.Context, f *models.VideoFile, txnMgr txn.Manager, w CaptionUpdater) error {
	captions, err := w.GetCaptions(ctx, f.ID)
	if err != nil {
		return fmt.Errorf("getting captions for file %s: %w", f.Path, err)
	}

	if len(captions) == 0 {
		return nil
	}

	filePath := f.Path

	changed := false
	var newCaptions []*models.VideoCaption

	for _, caption := range captions {
		captionPath := caption.Path(filePath)
		_, err := os.Stat(captionPath)
		if errors.Is(err, os.ErrNotExist) {
			logger.Infof("Removing non existent caption %s for %s", caption.Filename, f.Path)
			changed = true
		} else {
			// other errors are ignored for the purposes of cleaning
			newCaptions = append(newCaptions, caption)
		}
	}

	if changed {
		fn := func(ctx context.Context) error {
			return w.UpdateCaptions(ctx, f.ID, newCaptions)
		}

		// possible that we are already in a transaction and txnMgr is nil
		// in that case just call the function directly
		if txnMgr == nil {
			err = fn(ctx)
		} else {
			err = txn.WithTxn(ctx, txnMgr, fn)
		}

		if err != nil {
			return fmt.Errorf("updating captions for file %s: %w", f.Path, err)
		}
	}

	return nil
}
