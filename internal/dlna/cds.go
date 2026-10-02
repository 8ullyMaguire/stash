package dlna

// from https://github.com/rclone/rclone
// Copyright (C) 2012 by Nick Craig-Wood http://www.craig-wood.com/nick/

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/dms/dlna"
	"github.com/anacrolix/dms/upnp"
	"github.com/anacrolix/dms/upnpav"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
)

var pageSize = 100

type browse struct {
	ObjectID       string
	BrowseFlag     string
	Filter         string
	StartingIndex  int
	RequestedCount int
}

type contentDirectoryService struct {
	*Server
	upnp.Eventing
}

func formatDurationSexagesimal(d time.Duration) string {
	ns := d % time.Second
	d /= time.Second
	s := d % 60
	d /= 60
	m := d % 60
	d /= 60
	h := d
	ret := fmt.Sprintf("%d:%02d:%02d.%09d", h, m, s, ns)
	ret = strings.TrimRight(ret, "0")
	ret = strings.TrimRight(ret, ".")
	return ret
}

func (me *contentDirectoryService) updateIDString() string {
	return fmt.Sprintf("%d", uint32(os.Getpid()))
}

func sceneToContainer(scene *models.Scene, parent string, host string) interface{} {
	// make stash server URL
	// TODO - fix this
	iconURI := (&url.URL{
		Scheme: "http",
		Host:   host,
		Path:   iconPath,
		RawQuery: url.Values{
			"scene": {strconv.Itoa(scene.ID)},
		}.Encode(),
	}).String()

	// Object goes first
	obj := upnpav.Object{
		ID:          strconv.Itoa(scene.ID),
		Restricted:  1,
		ParentID:    parent,
		Title:       scene.GetTitle(),
		Class:       "object.item.videoItem",
		Icon:        iconURI,
		AlbumArtURI: iconURI,
	}

	// Wrap up
	item := upnpav.Item{
		Object: obj,
		Res:    make([]upnpav.Resource, 0, 1),
	}

	mimeType := "video/mp4"
	var (
		size     int
		bitrate  uint
		duration int64
	)

	f := scene.Files.Primary()
	if f != nil {
		size = int(f.Size)
		bitrate = uint(f.BitRate)
		duration = int64(f.Duration)
	}

	item.Res = append(item.Res, upnpav.Resource{
		URL: (&url.URL{
			Scheme: "http",
			Host:   host,
			Path:   resPath,
			RawQuery: url.Values{
				"scene": {strconv.Itoa(scene.ID)},
			}.Encode(),
		}).String(),
		ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", mimeType, dlna.ContentFeatures{
			SupportRange: true,
		}.String()),
		Bitrate:  bitrate,
		Duration: formatDurationSexagesimal(time.Duration(duration) * time.Second),
		Size:     uint64(size),
		// Resolution: resolution,
	})

	item.Res = append(item.Res, upnpav.Resource{
		URL:          iconURI,
		ProtocolInfo: "http-get:*:image/jpeg:DLNA.ORG_PN=JPEG_MED",
	})

	return item
}

// ContentDirectory object from ObjectID.
func (me *contentDirectoryService) objectFromID(id string) (o object, err error) {
	o.Path, err = url.QueryUnescape(id)
	if err != nil {
		return
	}
	if o.Path == "0" {
		o.Path = "/"
	}
	// o.Path = path.Clean(o.Path)
	// if !path.IsAbs(o.Path) {
	// 	err = fmt.Errorf("bad ObjectID %v", o.Path)
	// 	return
	// }
	o.RootObjectPath = me.RootObjectPath

	return
}

func childPath(paths []string) []string {
	if len(paths) > 1 {
		return paths[1:]
	}

	return nil
}

func (me *contentDirectoryService) Handle(action string, argsXML []byte, r *http.Request) (map[string]string, error) {
	host := r.Host
	// userAgent := r.UserAgent()
	switch action {
	case "GetSystemUpdateID":
		return map[string]string{
			"Id": me.updateIDString(),
		}, nil
	case "GetSortCapabilities":
		return map[string]string{
			"SortCaps": "dc:title",
		}, nil
	case "Browse":
		var browse browse
		if err := xml.Unmarshal([]byte(argsXML), &browse); err != nil {
			return nil, upnp.Errorf(upnp.ArgumentValueInvalidErrorCode, "cannot unmarshal browse argument: %s", err.Error())
		}

		obj, err := me.objectFromID(browse.ObjectID)
		if err != nil {
			return nil, upnp.Errorf(upnpav.NoSuchObjectErrorCode, "cannot find object with id %q: %v", browse.ObjectID, err.Error())
		}

		switch browse.BrowseFlag {
		case "BrowseDirectChildren":
			return me.handleBrowseDirectChildren(obj, host)
		case "BrowseMetadata":
			return me.handleBrowseMetadata(obj, host)
		default:
			return nil, upnp.Errorf(upnp.ArgumentValueInvalidErrorCode, "unhandled browse flag: %v", browse.BrowseFlag)
		}
	case "GetSearchCapabilities":
		return map[string]string{
			"SearchCaps": "",
		}, nil
	// from https://github.com/rclone/rclone/blob/master/cmd/serve/dlna/cds.go
	// Samsung Extensions
	case "X_GetFeatureList":
		return map[string]string{
			"FeatureList": `<Features xmlns="urn:schemas-upnp-org:av:avs" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="urn:schemas-upnp-org:av:avs http://www.upnp.org/schemas/av/avs.xsd">
	<Feature name="samsung.com_BASICVIEW" version="1">
		<container id="0" type="object.item.imageItem"/>
		<container id="0" type="object.item.audioItem"/>
		<container id="0" type="object.item.videoItem"/>
	</Feature>
	</Features>`}, nil
	case "X_SetBookmark":
		// just ignore
		return map[string]string{}, nil
	default:
		return nil, upnp.InvalidActionError
	}
}

func (me *contentDirectoryService) handleBrowseDirectChildren(obj object, host string) (map[string]string, error) {
	// Read folder and return children
	// TODO: check if obj == 0 and return root objects
	// TODO: check if special path and return files

	var objs []interface{}

	if obj.IsRoot() {
		objs = getRootObjects()
	}

	paths := strings.Split(obj.Path, "/")

	// All videos
	if obj.Path == "all" {
		objs = me.getAllScenes(host)
	}

	if strings.HasPrefix(obj.Path, "all/") {
		page := getPageFromID(paths)
		if page != nil {
			objs = me.getPageVideos(&models.SceneFilterType{}, "all", *page, host)
		}
	}

	// Saved searches
	// if obj.Path == "saved-searches" {
	// 	var savedPlaylists []models.Playlist
	// 	db, _ := models.GetDB()
	// 	db.Where("is_deo_enabled = ?", true).Order("ordering asc").Find(&savedPlaylists)
	// 	db.Close()

	// 	for _, playlist := range savedPlaylists {
	// 		objs = append(objs, upnpav.Container{Object: upnpav.Object{
	// 			ID:         "saved-searches/" + strconv.Itoa(int(playlist.ID)),
	// 			Restricted: 1,
	// 			ParentID:   "saved-searches",
	// 			Class:      "object.container.storageFolder",
	// 			Title:      playlist.Name,
	// 		}})
	// 	}
	// }

	// if strings.HasPrefix(obj.Path, "saved-searches/") {
	// 	id := strings.Split(obj.Path, "/")

	// 	var savedPlaylist models.Playlist
	// 	db, _ := models.GetDB()
	// 	db.Where("id = ?", id[1]).First(&savedPlaylist)
	// 	db.Close()

	// 	var r models.RequestSceneList
	// 	if err := json.Unmarshal([]byte(savedPlaylist.SearchParams), &r); err == nil {
	// 		r.IsAccessible = optional.NewBool(true)
	// 		r.IsAvailable = optional.NewBool(true)
	// 		data := models.QueryScenesFull(r)

	// 		for i := range data.Scenes {
	// 			objs = append(objs, me.sceneToContainer(data.Scenes[i], "sites/"+id[1], host))
	// 		}
	// 	}
	// }

	// Studios
	if obj.Path == "studios" {
		objs = me.getStudios()
	}

	if strings.HasPrefix(obj.Path, "studios/") {
		objs = me.getStudioScenes(childPath(paths), host)
	}

	// Tags
	if obj.Path == "tags" {
		objs = me.getTags()
	}

	if strings.HasPrefix(obj.Path, "tags/") {
		objs = me.getTagScenes(childPath(paths), host)
	}

	// Performers
	if obj.Path == "performers" {
		objs = me.getPerformers()
	}

	if strings.HasPrefix(obj.Path, "performers/") {
		objs = me.getPerformerScenes(childPath(paths), host)
	}

	// Groups - deprecated
	if obj.Path == "groups" {
		objs = me.getGroups()
	}

	if strings.HasPrefix(obj.Path, "groups/") {
		objs = me.getGroupScenes(childPath(paths), host)
	}

	// Rating
	if obj.Path == "rating" {
		objs = me.getRating()
	}

	if strings.HasPrefix(obj.Path, "rating/") {
		objs = me.getRatingScenes(childPath(paths), host)
	}

	// stash#1580 -- see the folder list in getRootObjects.
	if strings.HasPrefix(obj.Path, "recently-added/") {
		objs = me.getRecentScenes(childPath(paths), obj.Path, host, recentFilterAdded)
	}
	if strings.HasPrefix(obj.Path, "recently-played/") {
		objs = me.getRecentScenes(childPath(paths), obj.Path, host, recentFilterPlayed)
	}
	if strings.HasPrefix(obj.Path, "unplayed/") {
		objs = me.getRecentScenes(childPath(paths), obj.Path, host, recentFilterUnplayed)
	}
	// The three containers themselves are leaves holding a query: browsing one returns its
	// scenes directly rather than a further level of subfolders.
	if obj.Path == "recently-added" {
		objs = me.getRecentScenes(nil, obj.Path, host, recentFilterAdded)
	}
	if obj.Path == "recently-played" {
		objs = me.getRecentScenes(nil, obj.Path, host, recentFilterPlayed)
	}
	if obj.Path == "unplayed" {
		objs = me.getRecentScenes(nil, obj.Path, host, recentFilterUnplayed)
	}

	return makeBrowseResult(objs, me.updateIDString())
}

func (me *contentDirectoryService) handleBrowseMetadata(obj object, host string) (map[string]string, error) {
	var objs []interface{}
	var updateID string

	// if numeric, then must be scene, otherwise handle as if path
	sceneID, err := strconv.Atoi(obj.Path)
	if err != nil {
		// #1465 - handle root object
		if obj.IsRoot() {
			objs = getRootObject()
		} else {
			// HACK: just create a fake storage folder to return. The name won't
			// be correct, but hopefully the names returned from handleBrowseDirectChildren
			// will be used instead.
			objs = []interface{}{makeStorageFolder(obj.ID(), obj.ID(), obj.ParentID())}
		}

		updateID = me.updateIDString()
	} else {
		var scene *models.Scene

		r := me.repository
		if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
			scene, err = r.SceneFinder.Find(ctx, sceneID)
			if scene != nil {
				err = scene.LoadPrimaryFile(ctx, r.FileGetter)
			}

			if err != nil {
				return err
			}

			return nil
		}); err != nil {
			logger.Error(err.Error())
		}

		if scene != nil {
			upnpObject := sceneToContainer(scene, "-1", host)
			objs = []interface{}{upnpObject}

			// http://upnp.org/specs/av/UPnP-av-ContentDirectory-v1-Service.pdf
			// maximum update ID is 2**32, then rolls back to 0
			const maxUpdateID int64 = 1 << 32
			updateID = fmt.Sprint(scene.UpdatedAt.Unix() % maxUpdateID)
		} else {
			return nil, upnp.Errorf(upnpav.NoSuchObjectErrorCode, "scene not found")
		}
	}

	return makeBrowseResult(objs, updateID)
}

func makeBrowseResult(objs []interface{}, updateID string) (map[string]string, error) {
	result, err := xml.Marshal(objs)
	if err != nil {
		return nil, upnp.Errorf(upnp.ActionFailedErrorCode, "could not marshal objects: %s", err.Error())
	}

	return map[string]string{
		"TotalMatches":   fmt.Sprint(len(objs)),
		"NumberReturned": fmt.Sprint(len(objs)),
		"Result":         didl_lite(string(result)),
		"UpdateID":       updateID,
	}, nil
}

func makeStorageFolder(id, title, parentID string) upnpav.Container {
	defaultChildCount := 1
	return upnpav.Container{
		Object: upnpav.Object{
			ID:         id,
			Restricted: 1,
			ParentID:   parentID,
			Class:      "object.container.storageFolder",
			Title:      title,
		},
		ChildCount: defaultChildCount,
	}
}

func getRootObject() []interface{} {
	const rootID = "0"

	return []interface{}{makeStorageFolder(rootID, "stash", "-1")}
}

func getRootObjects() []interface{} {
	const rootID = "0"

	var objs []interface{}

	objs = append(objs, makeStorageFolder("all", "all", rootID))
	objs = append(objs, makeStorageFolder("performers", "performers", rootID))
	objs = append(objs, makeStorageFolder("tags", "tags", rootID))
	objs = append(objs, makeStorageFolder("studios", "studios", rootID))
	objs = append(objs, makeStorageFolder("groups", "groups", rootID))
	objs = append(objs, makeStorageFolder("rating", "rating", rootID))
	// stash#1580 -- recently added / viewed / unplayed, as read-only virtual containers.
	// They are folders holding a query rather than a parent of stored objects, so they cost
	// nothing on disk and cannot be written to: nothing in the browse path accepts them as a
	// parentID for anything but a scene listing.
	objs = append(objs, makeStorageFolder("recently-added", "recently added", rootID))
	objs = append(objs, makeStorageFolder("recently-played", "recently played", rootID))
	objs = append(objs, makeStorageFolder("unplayed", "unplayed", rootID))

	return objs
}

func getSortDirection(sceneFilter *models.SceneFilterType, sort string) models.SortDirectionEnum {
	direction := models.SortDirectionEnumDesc
	if sort == "title" {
		direction = models.SortDirectionEnumAsc
	}

	return direction
}

func (me *contentDirectoryService) getVideos(sceneFilter *models.SceneFilterType, parentID string, host string) []interface{} {
	var objs []interface{}

	r := me.repository
	if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
		sort := me.VideoSortOrder
		direction := getSortDirection(sceneFilter, sort)
		findFilter := &models.FindFilterType{
			PerPage:   &pageSize,
			Sort:      &sort,
			Direction: &direction,
		}

		scenes, total, err := scene.QueryWithCount(ctx, r.SceneFinder, sceneFilter, findFilter)
		if err != nil {
			return err
		}

		if total > pageSize {
			pager := scenePager{
				sceneFilter: sceneFilter,
				parentID:    parentID,
			}

			objs, err = pager.getPages(ctx, r.SceneFinder, total)
			if err != nil {
				return err
			}
		} else {
			for _, s := range scenes {
				if err := s.LoadPrimaryFile(ctx, r.FileGetter); err != nil {
					return err
				}

				objs = append(objs, sceneToContainer(s, parentID, host))
			}
		}

		return nil
	}); err != nil {
		logger.Error(err.Error())
	}

	return objs
}

func (me *contentDirectoryService) getPageVideos(sceneFilter *models.SceneFilterType, parentID string, page int, host string) []interface{} {
	var objs []interface{}

	r := me.repository
	if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
		pager := scenePager{
			sceneFilter: sceneFilter,
			parentID:    parentID,
		}

		sort := me.VideoSortOrder
		direction := getSortDirection(sceneFilter, sort)
		var err error
		objs, err = pager.getPageVideos(ctx, r.SceneFinder, r.FileGetter, page, host, sort, direction)
		if err != nil {
			return err
		}

		return nil
	}); err != nil {
		logger.Error(err.Error())
	}

	return objs
}

func getPageFromID(paths []string) *int {
	i := slices.Index(paths, "page")
	if i == -1 || i+1 >= len(paths) {
		return nil
	}

	ret, err := strconv.Atoi(paths[i+1])
	if err != nil {
		return nil
	}

	return &ret
}

func (me *contentDirectoryService) getAllScenes(host string) []interface{} {
	return me.getVideos(&models.SceneFilterType{}, "all", host)
}

func (me *contentDirectoryService) getStudios() []interface{} {
	var objs []interface{}

	r := me.repository
	if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
		studios, err := r.StudioFinder.All(ctx)
		if err != nil {
			return err
		}

		for _, s := range studios {
			objs = append(objs, makeStorageFolder("studios/"+strconv.Itoa(s.ID), s.Name, "studios"))
		}

		return nil
	}); err != nil {
		logger.Errorf(err.Error())
	}

	return objs
}

func (me *contentDirectoryService) getStudioScenes(paths []string, host string) []interface{} {
	sceneFilter := &models.SceneFilterType{
		Studios: &models.HierarchicalMultiCriterionInput{
			Modifier: models.CriterionModifierIncludes,
			Value:    []string{paths[0]},
		},
	}

	parentID := "studios/" + strings.Join(paths, "/")

	page := getPageFromID(paths)
	if page != nil {
		return me.getPageVideos(sceneFilter, parentID, *page, host)
	}

	return me.getVideos(sceneFilter, parentID, host)
}

func (me *contentDirectoryService) getTags() []interface{} {
	var objs []interface{}

	r := me.repository
	if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
		tags, err := r.TagFinder.All(ctx)
		if err != nil {
			return err
		}

		for _, s := range tags {
			objs = append(objs, makeStorageFolder("tags/"+strconv.Itoa(s.ID), s.Name, "tags"))
		}

		return nil
	}); err != nil {
		logger.Errorf(err.Error())
	}

	return objs
}

func (me *contentDirectoryService) getTagScenes(paths []string, host string) []interface{} {
	sceneFilter := &models.SceneFilterType{
		Tags: &models.HierarchicalMultiCriterionInput{
			Modifier: models.CriterionModifierIncludes,
			Value:    []string{paths[0]},
		},
	}

	parentID := "tags/" + strings.Join(paths, "/")

	page := getPageFromID(paths)
	if page != nil {
		return me.getPageVideos(sceneFilter, parentID, *page, host)
	}

	return me.getVideos(sceneFilter, parentID, host)
}

func (me *contentDirectoryService) getPerformers() []interface{} {
	var objs []interface{}

	r := me.repository
	if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
		performers, err := r.PerformerFinder.All(ctx)
		if err != nil {
			return err
		}

		for _, s := range performers {
			objs = append(objs, makeStorageFolder("performers/"+strconv.Itoa(s.ID), s.Name, "performers"))
		}

		return nil
	}); err != nil {
		logger.Errorf(err.Error())
	}

	return objs
}

func (me *contentDirectoryService) getPerformerScenes(paths []string, host string) []interface{} {
	sceneFilter := &models.SceneFilterType{
		Performers: &models.MultiCriterionInput{
			Modifier: models.CriterionModifierIncludes,
			Value:    []string{paths[0]},
		},
	}

	parentID := "performers/" + strings.Join(paths, "/")

	page := getPageFromID(paths)
	if page != nil {
		return me.getPageVideos(sceneFilter, parentID, *page, host)
	}

	return me.getVideos(sceneFilter, parentID, host)
}

func (me *contentDirectoryService) getGroups() []interface{} {
	var objs []interface{}

	r := me.repository
	if err := r.WithReadTxn(context.TODO(), func(ctx context.Context) error {
		groups, err := r.GroupFinder.All(ctx)
		if err != nil {
			return err
		}

		for _, s := range groups {
			objs = append(objs, makeStorageFolder("groups/"+strconv.Itoa(s.ID), s.Name, "groups"))
		}

		return nil
	}); err != nil {
		logger.Errorf(err.Error())
	}

	return objs
}

func (me *contentDirectoryService) getGroupScenes(paths []string, host string) []interface{} {
	sceneFilter := &models.SceneFilterType{
		Groups: &models.HierarchicalMultiCriterionInput{
			Modifier: models.CriterionModifierIncludes,
			Value:    []string{paths[0]},
		},
	}

	parentID := "groups/" + strings.Join(paths, "/")

	page := getPageFromID(paths)
	if page != nil {
		return me.getPageVideos(sceneFilter, parentID, *page, host)
	}

	return me.getVideos(sceneFilter, parentID, host)
}

func (me *contentDirectoryService) getRating() []interface{} {
	var objs []interface{}

	for r := 1; r <= 5; r++ {
		rStr := strconv.Itoa(r)
		objs = append(objs, makeStorageFolder("rating/"+rStr, rStr, "rating"))
	}

	return objs
}

func (me *contentDirectoryService) getRatingScenes(paths []string, host string) []interface{} {
	r, err := strconv.Atoi(paths[0])
	if err != nil {
		return nil
	}

	sceneFilter := &models.SceneFilterType{
		Rating100: &models.IntCriterionInput{
			Modifier: models.CriterionModifierEquals,
			Value:    models.Rating5To100(r),
		},
	}

	parentID := "rating/" + strings.Join(paths, "/")

	page := getPageFromID(paths)
	if page != nil {
		return me.getPageVideos(sceneFilter, parentID, *page, host)
	}

	return me.getVideos(sceneFilter, parentID, host)
}

// stash#1580 -- "DLNA folders: recently added, viewed, unplayed".
//
// Three read-only virtual containers, each a folder holding a query rather than a parent of
// stored objects. The alternative -- a fourth sub-level under `all` -- would be a deeper tree
// for the same scenes, and DLNA clients vary in how many levels they will walk, so the
// shallowest useful depth is the one that works.
//
// The three filters are deliberately DIFFERENT columns rather than one "recent" notion:
//
//	recently added   created_at     -- when the scan found it
//	recently played  last_played_at  -- when someone watched it
//	unplayed         play_count     -- whether anyone ever finished it
//
// "Unplayed" is NOT `play_count = 0`. A scene opened and abandoned has play_count 0 but
// last_played_at set, and a scene watched to the end has play_count 1 and last_played_at set;
// treating either as unplayed lists scenes the user has demonstrably seen. `IS_NULL` on
// play_count asks the question the folder name asks -- has this ever been played at all --
// which is the only reading under which the container is not a duplicate of "recently played".
//
// Both timestamps descend from NOW rather than being computed in Go, so a client that sits on
// the folder open across midnight sees a different window than one that opened it earlier,
// which is the behaviour a "recently" label implies.
type recentFilterKind int

const (
	recentFilterAdded recentFilterKind = iota
	recentFilterPlayed
	recentFilterUnplayed
)

// recentWindow is how far back the two timestamp folders look. A fixed window rather than a
// count limit: "recently added" on a TV should mean this week, not whichever 60 scenes happen
// to sort highest, and a count limit silently changes meaning as the library grows.
const recentWindow = "7 days ago"

func recentSceneFilter(kind recentFilterKind) *models.SceneFilterType {
	switch kind {
	case recentFilterAdded:
		return &models.SceneFilterType{
			CreatedAt: &models.TimestampCriterionInput{
				Modifier: models.CriterionModifierGreaterThan,
				Value:    recentWindow,
			},
		}
	case recentFilterPlayed:
		return &models.SceneFilterType{
			LastPlayedAt: &models.TimestampCriterionInput{
				Modifier: models.CriterionModifierGreaterThan,
				Value:    recentWindow,
			},
		}
	case recentFilterUnplayed:
		// `play_count` is NULL until a scene is played for the first time, so IS_NULL is
		// "never played" rather than "played zero times".
		return &models.SceneFilterType{
			PlayCount: &models.IntCriterionInput{
				Modifier: models.CriterionModifierIsNull,
			},
		}
	default:
		return &models.SceneFilterType{}
	}
}

// getRecentScenes serves all three containers. `paths` is the child path, so the two-level
// form is "recently-added/page/2" and is the same paging the `all` folder already does; the
// one-level form is the container itself, which is a leaf.
func (me *contentDirectoryService) getRecentScenes(paths []string, parentID string, host string, kind recentFilterKind) []interface{} {
	sceneFilter := recentSceneFilter(kind)

	page := getPageFromID(paths)
	if page != nil {
		return me.getPageVideos(sceneFilter, parentID, *page, host)
	}

	return me.getVideos(sceneFilter, parentID, host)
}

// Represents a ContentDirectory object.
type object struct {
	Path           string // The cleaned, absolute path for the object relative to the server.
	RootObjectPath string
}

// Returns the actual local filesystem path for the object.
func (o *object) FilePath() string {
	return filepath.Join(o.RootObjectPath, filepath.FromSlash(o.Path))
}

// Returns the ObjectID for the object. This is used in various ContentDirectory actions.
func (o object) ID() string {
	if len(o.Path) == 1 {
		return "0"
	}
	return url.QueryEscape(o.Path)
}

func (o *object) IsRoot() bool {
	return o.Path == "/"
}

// Returns the object's parent ObjectID. Fortunately it can be deduced from the
// ObjectID (for now).
func (o object) ParentID() string {
	if o.IsRoot() {
		return "-1"
	}
	o.Path = path.Dir(o.Path)
	return o.ID()
}
