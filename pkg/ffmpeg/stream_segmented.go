package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/signedurl"
	"github.com/stashapp/stash/pkg/utils"

	"github.com/zencoder/go-dash/v3/mpd"
)

const (
	MimeHLS    string = "application/vnd.apple.mpegurl"
	MimeMpegTS string = "video/MP2T"
	MimeDASH   string = "application/dash+xml"

	segmentLength = 2

	maxSegmentWait  = 15 * time.Second
	monitorInterval = 200 * time.Millisecond

	// segment gap before counting a request as a seek and
	// restarting the transcode process at the requested segment
	maxSegmentGap = 5

	// maximum number of segments to generate
	// ahead of the currently streaming segment
	maxSegmentBuffer = 15

	// maximum idle time between segment requests before
	// stopping transcode and deleting cache folder
	maxIdleTime = 30 * time.Second

	resolutionParamKey = "resolution"
	// TODO - setting the apikey in here isn't ideal
	apiKeyParamKey = "apikey"
)

type StreamType struct {
	Name          string
	SegmentType   *SegmentType
	ServeManifest func(sm *StreamManager, w http.ResponseWriter, r *http.Request, vf *models.VideoFile, resolution string)
	Args          func(codec VideoCodec, segment int, videoFilter VideoFilter, videoOnly bool, outputDir string) Args
}

var (
	StreamTypeHLS = &StreamType{
		Name:          "hls",
		SegmentType:   SegmentTypeTS,
		ServeManifest: serveHLSManifest,
		Args: func(codec VideoCodec, segment int, videoFilter VideoFilter, videoOnly bool, outputDir string) (args Args) {
			args = CodecInit(codec)
			args = append(args,
				"-flags", "+cgop",
				"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segmentLength),
			)
			args = args.VideoFilter(videoFilter)
			if videoOnly {
				args = append(args, "-an")
			} else {
				args = append(args,
					"-c:a", "aac",
					"-ac", "2",
				)
			}
			args = append(args,
				"-sn",
				"-copyts",
				"-avoid_negative_ts", "disabled",
				"-f", "hls",
				"-start_number", fmt.Sprint(segment),
				"-hls_time", fmt.Sprint(segmentLength),
				"-hls_flags", "split_by_time",
				"-hls_segment_type", "mpegts",
				"-hls_playlist_type", "vod",
				"-hls_segment_filename", filepath.Join(outputDir, ".%d.ts"),
				filepath.Join(outputDir, "manifest.m3u8"),
			)
			return
		},
	}
	StreamTypeHLSCopy = &StreamType{
		Name:          "hls-copy",
		SegmentType:   SegmentTypeTS,
		ServeManifest: serveHLSManifest,
		Args: func(codec VideoCodec, segment int, videoFilter VideoFilter, videoOnly bool, outputDir string) (args Args) {
			args = CodecInit(codec)
			if videoOnly {
				args = append(args, "-an")
			} else {
				args = append(args,
					"-c:a", "aac",
					"-ac", "2",
				)
			}
			args = append(args,
				"-sn",
				"-copyts",
				"-avoid_negative_ts", "disabled",
				"-f", "hls",
				"-start_number", fmt.Sprint(segment),
				"-hls_time", fmt.Sprint(segmentLength),
				"-hls_flags", "split_by_time",
				"-hls_segment_type", "mpegts",
				"-hls_playlist_type", "vod",
				"-hls_segment_filename", filepath.Join(outputDir, ".%d.ts"),
				filepath.Join(outputDir, "manifest.m3u8"),
			)
			return
		},
	}
	StreamTypeDASHVideo = &StreamType{
		Name:          "dash-v",
		SegmentType:   SegmentTypeWEBMVideo,
		ServeManifest: serveDASHManifest,
		Args: func(codec VideoCodec, segment int, videoFilter VideoFilter, videoOnly bool, outputDir string) (args Args) {
			// only generate the actual init segment (init_v.webm)
			// when generating the first segment
			init := ".init"
			if segment == 0 {
				init = "init"
			}

			args = CodecInit(codec)
			args = append(args,
				"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segmentLength),
			)

			args = args.VideoFilter(videoFilter)
			args = append(args,
				"-copyts",
				"-avoid_negative_ts", "disabled",
				"-map", "0:v:0",
				"-f", "webm_chunk",
				"-chunk_start_index", fmt.Sprint(segment),
				"-header", filepath.Join(outputDir, init+"_v.webm"),
				filepath.Join(outputDir, ".%d_v.webm"),
			)
			return
		},
	}
	StreamTypeDASHAudio = &StreamType{
		Name:          "dash-a",
		SegmentType:   SegmentTypeWEBMAudio,
		ServeManifest: serveDASHManifest,
		Args: func(codec VideoCodec, segment int, videoFilter VideoFilter, videoOnly bool, outputDir string) (args Args) {
			// only generate the actual init segment (init_a.webm)
			// when generating the first segment
			init := ".init"
			if segment == 0 {
				init = "init"
			}
			args = append(args,
				"-c:a", "libopus",
				"-b:a", "96000",
				"-ar", "48000",
				"-copyts",
				"-avoid_negative_ts", "disabled",
				"-map", "0:a:0",
				"-f", "webm_chunk",
				"-chunk_start_index", fmt.Sprint(segment),
				"-audio_chunk_duration", fmt.Sprint(segmentLength*1000),
				"-header", filepath.Join(outputDir, init+"_a.webm"),
				filepath.Join(outputDir, ".%d_a.webm"),
			)
			return
		},
	}
)

type SegmentType struct {
	Format       string
	MimeType     string
	MakeFilename func(segment int) string
	ParseSegment func(str string) (int, error)
}

var (
	SegmentTypeTS = &SegmentType{
		Format:   "%d.ts",
		MimeType: MimeMpegTS,
		MakeFilename: func(segment int) string {
			return fmt.Sprintf("%d.ts", segment)
		},
		ParseSegment: func(str string) (int, error) {
			segment, err := strconv.Atoi(str)
			if err != nil || segment < 0 {
				err = ErrInvalidSegment
			}
			return segment, err
		},
	}
	SegmentTypeWEBMVideo = &SegmentType{
		Format:   "%d_v.webm",
		MimeType: MimeWebmVideo,
		MakeFilename: func(segment int) string {
			if segment == -1 {
				return "init_v.webm"
			} else {
				return fmt.Sprintf("%d_v.webm", segment)
			}
		},
		ParseSegment: func(str string) (int, error) {
			if str == "init" {
				return -1, nil
			} else {
				segment, err := strconv.Atoi(str)
				if err != nil || segment < 0 {
					err = ErrInvalidSegment
				}
				return segment, err
			}
		},
	}
	SegmentTypeWEBMAudio = &SegmentType{
		Format:   "%d_a.webm",
		MimeType: MimeWebmAudio,
		MakeFilename: func(segment int) string {
			if segment == -1 {
				return "init_a.webm"
			} else {
				return fmt.Sprintf("%d_a.webm", segment)
			}
		},
		ParseSegment: func(str string) (int, error) {
			if str == "init" {
				return -1, nil
			} else {
				segment, err := strconv.Atoi(str)
				if err != nil || segment < 0 {
					err = ErrInvalidSegment
				}
				return segment, err
			}
		},
	}
)

var ErrInvalidSegment = errors.New("invalid segment")

type StreamOptions struct {
	StreamType *StreamType
	VideoFile  *models.VideoFile
	Resolution string
	Hash       string
	Segment    string
}

type transcodeProcess struct {
	cmd         *exec.Cmd
	context     context.Context
	cancel      context.CancelFunc
	cancelled   bool
	outputDir   string
	segmentType *SegmentType
	segment     int
}

type waitingSegment struct {
	segmentType *SegmentType
	idx         int
	file        string
	path        string
	accessed    time.Time
	available   chan error
	done        atomic.Bool
}

type runningStream struct {
	dir              string
	streamType       *StreamType
	vf               *models.VideoFile
	maxTranscodeSize int
	outputDir        string

	waitingSegments []*waitingSegment
	tp              *transcodeProcess
	lastAccessed    time.Time
	lastSegment     int
}

func (t StreamType) String() string {
	return t.Name
}

// #3530 - the segment cache key.
//
// The directory is keyed on the SCENE hash, which is derived from the FILE. That was harmless when
// a scene WAS a file, but #3530 lets one file back several scenes with different windows, so two
// such scenes share a directory and whichever is transcoded first populates segments the other
// then serves -- silently, because the filenames and the playlist both look correct.
//
// The window is therefore part of the key.
//
// It is a struct with IsZero rather than two floats because `0-0` is a LEGAL window (the head of
// the file, ending early) and must NOT collapse onto "no window" -- a scene of 0-4s of a 2-hour
// file is nothing like the whole file, and sharing a cache directory with unranged scenes is the
// exact bug being fixed here.
//
// An UNRANGED scene produces the empty key, so its directory name is UNCHANGED and no existing
// cache is invalidated by this commit. That is the reason the empty case is spelled out rather than
// left to fall out of a format string.
type windowKey struct {
	start float64
	end   float64
	set   bool
}

func windowKeyOf(vf *models.VideoFile) windowKey {
	if vf == nil || (vf.StartTime == nil && vf.EndTime == nil) {
		return windowKey{}
	}
	k := windowKey{set: true}
	if vf.StartTime != nil {
		k.start = *vf.StartTime
	}
	if vf.EndTime != nil {
		k.end = *vf.EndTime
	}
	return k
}

// windowLength is the scene's window length in seconds, or 0 when unbounded.
//
// vf.Duration is ALREADY this value for a ranged scene -- GetFiles returns a per-scene copy
// whose Duration is the derived window length. It is read here rather than recomputed so the
// length used for the cache key, the -t, and the segment count cannot drift apart.
func windowLength(vf *models.VideoFile) float64 {
	k := windowKeyOf(vf)
	if !k.set {
		return 0
	}
	length := k.end - k.start
	if k.end == 0 && vf != nil {
		// An open-ended window runs to the end of the file. vf.Duration has been reduced to
		// the window's length, and the start is known, so the file's length is recoverable.
		length = vf.Duration
	}
	if length < 0 {
		return 0
	}
	return length
}

func (t StreamType) FileDir(hash string, maxTranscodeSize int, window windowKey) string {
	// The base name is built EXACTLY as it was before #3530 when there is no window, so an
	// existing cache keeps working. The suffix is appended only when a window exists.
	base := hash
	if maxTranscodeSize == 0 {
		base = fmt.Sprintf("%s_%s", hash, t)
	} else {
		base = fmt.Sprintf("%s_%s_%d", hash, t, maxTranscodeSize)
	}
	if !window.set {
		return base
	}
	return fmt.Sprintf("%s_w%.3f-%.3f", base, window.start, window.end)
}

func HLSGetCodec(sm *StreamManager, name string) (codec VideoCodec) {
	switch name {
	case "hls":
		codec = VideoCodecLibX264
		if hwcodec := sm.encoder.hwCodecHLSCompatible(); hwcodec != nil && sm.config.GetTranscodeHardwareAcceleration() {
			codec = *hwcodec
		}
	case "dash-v":
		codec = VideoCodecVP9
		if hwcodec := sm.encoder.hwCodecWEBMCompatible(); hwcodec != nil && sm.config.GetTranscodeHardwareAcceleration() {
			codec = *hwcodec
		}
	case "hls-copy":
		codec = VideoCodecCopy
	}

	return codec
}

func (s *runningStream) makeStreamArgs(sm *StreamManager, segment int) Args {
	extraInputArgs := sm.config.GetLiveTranscodeInputArgs()
	extraOutputArgs := sm.config.GetLiveTranscodeOutputArgs()

	args := Args{"-hide_banner"}
	args = args.LogLevel(LogLevelError)

	codec := HLSGetCodec(sm, s.streamType.Name)

	fullhw := sm.config.GetTranscodeHardwareAcceleration() && sm.encoder.hwCanFullHWTranscode(sm.context, codec, s.vf, s.maxTranscodeSize)
	args = sm.encoder.hwDeviceInit(args, codec, fullhw)
	args = append(args, extraInputArgs...)

	// #3530 - a ranged scene's segments are numbered from the WINDOW's start, so the seek is the
	// window's start PLUS the segment's offset into it.
	//
	// The guard changed from `segment > 0` to `base != 0 || segment > 0`, and that is the whole
	// point: segment 0 of a window starting at 60s must ALSO seek to 60. Under the old guard it
	// did not, so the first segment came from the HEAD of the file -- 60s of footage before the
	// scene even begins.
	base := 0.0
	if s.vf != nil && s.vf.StartTime != nil {
		base = *s.vf.StartTime
	}
	if base != 0 || segment > 0 {
		args = args.Seek(base + float64(segment*segmentLength))
	}

	// And the process must STOP at the window's end. Without this ffmpeg runs to the end of the
	// FILE and writes the right segments into the right filenames, so the playlist looks correct
	// and a player requesting one segment past the end gets real footage instead of a 404.
	//
	// -t is a DURATION measured from the CURRENT position, so it is the window's remaining
	// length from this segment's start point -- not the window's total length. For segment 1 of
	// a 60..66 window that is 66 - 62 = 4, not 6.
	if length := windowLength(s.vf); length > 0 {
		args = args.Duration(length - float64(segment*segmentLength))
	}

	args = args.Input(s.vf.Path)

	videoOnly := ProbeAudioCodec(s.vf.AudioCodec) == MissingUnsupported

	videoFilter := sm.encoder.hwMaxResFilter(codec, s.vf, s.maxTranscodeSize, fullhw)

	args = append(args, s.streamType.Args(codec, segment, videoFilter, videoOnly, s.outputDir)...)

	args = append(args, extraOutputArgs...)

	return args
}

// checkSegments renames temp segments that have been completely generated.
// existing segments are not replaced - if a segment is generated
// multiple times, then only the first one is kept.
func (tp *transcodeProcess) checkSegments() {
	doSegment := func(filename string) {
		if filename != "" {
			oldPath := filepath.Join(tp.outputDir, filename)
			newPath := filepath.Join(tp.outputDir, filename[1:])
			if !segmentExists(newPath) {
				_ = os.Rename(oldPath, newPath)
			} else {
				os.Remove(oldPath)
			}
		}
	}

	processState := tp.cmd.ProcessState
	var lastFilename string
	for i := tp.segment; ; i++ {
		filename := fmt.Sprintf("."+tp.segmentType.Format, i)
		if segmentExists(filepath.Join(tp.outputDir, filename)) {
			// this segment exists so the previous segment is valid
			doSegment(lastFilename)
		} else {
			// if the transcode process has exited then
			// we need to do something with the last segment
			if processState != nil {
				if processState.Success() {
					// if the process exited successfully then
					// count the last segment as valid
					doSegment(lastFilename)
				} else if lastFilename != "" {
					// if the process exited unsuccessfully then just delete
					// the last segment, it's probably incomplete
					os.Remove(filepath.Join(tp.outputDir, lastFilename))
				}
			}
			break
		}

		lastFilename = filename
		tp.segment = i
	}
}

func lastSegment(vf *models.VideoFile) int {
	return int(math.Ceil(vf.Duration/segmentLength)) - 1
}

func segmentExists(path string) bool {
	exists, _ := fsutil.FileExists(path)
	return exists
}

// serveHLSManifest serves a generated HLS playlist. The URLs for the segments
// are of the form {r.URL}/%d.ts{?urlQuery} where %d is the segment index.
func serveHLSManifest(sm *StreamManager, w http.ResponseWriter, r *http.Request, vf *models.VideoFile, resolution string) {
	if sm.cacheDir == "" {
		logger.Error("[transcode] cannot live transcode with HLS because cache dir is unset")
		http.Error(w, "cannot live transcode with HLS because cache dir is unset", http.StatusServiceUnavailable)
		return
	}

	probeResult, err := sm.ffprobe.NewVideoFile(vf.Path)
	if err != nil {
		logger.Warnf("[transcode] error generating HLS manifest: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	prefix := r.Header.Get("X-Forwarded-Prefix")

	baseUrl := *r.URL
	baseUrl.RawQuery = ""
	baseURL := prefix + baseUrl.String()

	urlQuery := url.Values{}

	copyAuthParams(urlQuery, r.URL.Query())

	if resolution != "" {
		urlQuery.Set(resolutionParamKey, resolution)
	}

	segQuery := ""
	if len(urlQuery) > 0 {
		segQuery = "?" + urlQuery.Encode()
	}

	var buf bytes.Buffer

	fmt.Fprint(&buf, "#EXTM3U\n")
	fmt.Fprint(&buf, "#EXT-X-VERSION:3\n")
	fmt.Fprint(&buf, "#EXT-X-MEDIA-SEQUENCE:0\n")
	fmt.Fprintf(&buf, "#EXT-X-TARGETDURATION:%d\n", segmentLength)
	fmt.Fprint(&buf, "#EXT-X-PLAYLIST-TYPE:VOD\n")

	// #3530 - the playlist describes the SCENE, not the file.
	//
	// probeResult.FileDuration is the FILE's length, so an unranged scene is unaffected (the two
	// are the same number) which is exactly why a regression here is invisible on ordinary
	// content. vf.Duration is the WINDOW's length, because GetFiles returns a per-scene copy
	// whose Duration is the derived window length -- the same value lastSegment already uses.
	//
	// Both sites must agree: the segment COUNT comes from lastSegment (vf.Duration) and the
	// playlist LENGTH comes from here. If they disagree, the playlist advertises segments that
	// ServeSegment then rejects with 400, or omits ones it would happily serve.
	playlistDuration := probeResult.FileDuration
	if length := windowLength(vf); length > 0 {
		playlistDuration = length
	}

	leftover := playlistDuration
	segment := 0

	for leftover > 0 {
		thisLength := float64(segmentLength)
		if leftover < thisLength {
			thisLength = leftover
		}

		fmt.Fprintf(&buf, "#EXTINF:%f,\n", thisLength)
		fmt.Fprintf(&buf, "%s/%d.ts%s\n", baseURL, segment, segQuery)

		leftover -= thisLength
		segment++
	}

	fmt.Fprint(&buf, "#EXT-X-ENDLIST\n")

	w.Header().Set("Content-Type", MimeHLS)
	utils.ServeStaticContent(w, r, buf.Bytes())
}

// Forward auth params to segment URLs. API key takes precedence
// over signed params since it is explicitly configured by the user.
// TODO - this needs to be handled outside of this package
func copyAuthParams(dest url.Values, src url.Values) {
	apikey := src.Get(apiKeyParamKey)
	if apikey != "" {
		dest.Set(apiKeyParamKey, apikey)
	} else {
		cid := src.Get(signedurl.CIDParam)
		expires := src.Get(signedurl.ExpiresParam)
		sig := src.Get(signedurl.SigParam)
		if cid != "" && expires != "" && sig != "" {
			dest.Set(signedurl.CIDParam, cid)
			dest.Set(signedurl.ExpiresParam, expires)
			dest.Set(signedurl.SigParam, sig)
		}
	}
}

// serveDASHManifest serves a generated DASH manifest.
func serveDASHManifest(sm *StreamManager, w http.ResponseWriter, r *http.Request, vf *models.VideoFile, resolution string) {
	if sm.cacheDir == "" {
		logger.Error("[transcode] cannot live transcode with DASH because cache dir is unset")
		http.Error(w, "cannot live transcode files with DASH because cache dir is unset", http.StatusServiceUnavailable)
		return
	}

	probeResult, err := sm.ffprobe.NewVideoFile(vf.Path)
	if err != nil {
		logger.Warnf("[transcode] error generating DASH manifest: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var framerate string
	var videoWidth int
	var videoHeight int
	videoStream := probeResult.VideoStream
	if videoStream != nil {
		framerate = videoStream.AvgFrameRate
		videoWidth = videoStream.Width
		videoHeight = videoStream.Height
	} else {
		// extract the framerate fraction from the file framerate
		// framerates 0.1% below round numbers are common,
		// attempt to infer when this is the case
		fileFramerate := vf.FrameRate
		rate1001, off1001 := math.Modf(fileFramerate * 1.001)
		var numerator int
		var denominator int
		switch {
		case off1001 < 0.005:
			numerator = int(rate1001) * 1000
			denominator = 1001
		case off1001 > 0.995:
			numerator = (int(rate1001) + 1) * 1000
			denominator = 1001
		default:
			numerator = int(fileFramerate * 1000)
			denominator = 1000
		}
		framerate = fmt.Sprintf("%d/%d", numerator, denominator)
		videoHeight = vf.Height
		videoWidth = vf.Width
	}

	urlQuery := url.Values{}

	// Forward auth params to segment URLs. API key takes precedence
	// over signed params since it is explicitly configured by the user.
	// TODO - this needs to be handled outside of this package
	copyAuthParams(urlQuery, r.URL.Query())

	maxTranscodeSize := sm.config.GetMaxStreamingTranscodeSize().GetMaxResolution()
	if resolution != "" {
		maxTranscodeSize = models.StreamingResolutionEnum(resolution).GetMaxResolution()
		urlQuery.Set(resolutionParamKey, resolution)
	}
	if maxTranscodeSize != 0 {
		videoSize := videoHeight
		if videoWidth < videoSize {
			videoSize = videoWidth
		}

		if maxTranscodeSize < videoSize {
			scaleFactor := float64(maxTranscodeSize) / float64(videoSize)
			videoWidth = int(float64(videoWidth) * scaleFactor)
			videoHeight = int(float64(videoHeight) * scaleFactor)
		}
	}

	urlQueryString := ""
	if len(urlQuery) > 0 {
		urlQueryString = "?" + urlQuery.Encode()
	}

	// #3530 - as in serveHLSManifest: declare the SCENE's length, not the file's. See the note
	// there for why vf.Duration is the right value and why an unranged scene hides the change.
	manifestDuration := probeResult.FileDuration
	if length := windowLength(vf); length > 0 {
		manifestDuration = length
	}

	mediaDuration := mpd.Duration(time.Duration(manifestDuration * float64(time.Second)))
	m := mpd.NewMPD(mpd.DASH_PROFILE_LIVE, mediaDuration.String(), "PT4.0S")

	prefix := r.Header.Get("X-Forwarded-Prefix")

	baseUrl := r.URL.JoinPath("/")
	baseUrl.RawQuery = ""
	m.BaseURL = prefix + baseUrl.String()

	video, _ := m.AddNewAdaptationSetVideo(MimeWebmVideo, "progressive", true, 1)

	_, _ = video.SetNewSegmentTemplate(2, "init_v.webm"+urlQueryString, "$Number$_v.webm"+urlQueryString, 0, 1)
	_, _ = video.AddNewRepresentationVideo(200000, "vp09.00.40.08", "0", framerate, int64(videoWidth), int64(videoHeight))

	if ProbeAudioCodec(vf.AudioCodec) != MissingUnsupported {
		audio, _ := m.AddNewAdaptationSetAudio(MimeWebmAudio, true, 1, "und")
		_, _ = audio.SetNewSegmentTemplate(2, "init_a.webm"+urlQueryString, "$Number$_a.webm"+urlQueryString, 0, 1)
		_, _ = audio.AddNewRepresentationAudio(48000, 96000, "opus", "1")
	}

	var buf bytes.Buffer
	_ = m.Write(&buf)

	w.Header().Set("Content-Type", MimeDASH)
	utils.ServeStaticContent(w, r, buf.Bytes())
}

func (sm *StreamManager) ServeManifest(w http.ResponseWriter, r *http.Request, streamType *StreamType, vf *models.VideoFile, resolution string) {
	streamType.ServeManifest(sm, w, r, vf, resolution)
}

func (sm *StreamManager) serveWaitingSegment(w http.ResponseWriter, r *http.Request, segment *waitingSegment) {
	select {
	case <-r.Context().Done():
		break
	case err := <-segment.available:
		if err == nil {
			logger.Tracef("[transcode] streaming segment file %s", segment.file)
			w.Header().Set("Content-Type", segment.segmentType.MimeType)
			utils.ServeStaticFile(w, r, segment.path)
		} else if !errors.Is(err, context.Canceled) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
	segment.done.Store(true)
}

func (sm *StreamManager) ServeSegment(w http.ResponseWriter, r *http.Request, options StreamOptions) {
	if sm.cacheDir == "" {
		logger.Error("[transcode] cannot live transcode files because cache dir is unset")
		http.Error(w, "cannot live transcode files because cache dir is unset", http.StatusServiceUnavailable)
		return
	}

	if options.Hash == "" {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}

	streamType := options.StreamType

	segment, err := streamType.SegmentType.ParseSegment(options.Segment)
	// error if segment is past the end of the video
	if err != nil || segment > lastSegment(options.VideoFile) {
		http.Error(w, "invalid segment", http.StatusBadRequest)
		return
	}

	maxTranscodeSize := sm.config.GetMaxStreamingTranscodeSize().GetMaxResolution()
	if options.Resolution != "" {
		maxTranscodeSize = models.StreamingResolutionEnum(options.Resolution).GetMaxResolution()
	}

	dir := options.StreamType.FileDir(options.Hash, maxTranscodeSize, windowKeyOf(options.VideoFile))
	outputDir := filepath.Join(sm.cacheDir, dir)

	name := streamType.SegmentType.MakeFilename(segment)
	file := filepath.Join(dir, name)

	sm.streamsMutex.Lock()

	stream := sm.runningStreams[dir]
	if stream == nil {
		stream = &runningStream{
			dir:              dir,
			streamType:       options.StreamType,
			vf:               options.VideoFile,
			maxTranscodeSize: maxTranscodeSize,
			outputDir:        outputDir,

			// initialize to cap 10 to avoid reallocations
			waitingSegments: make([]*waitingSegment, 0, 10),
		}
		sm.runningStreams[dir] = stream
	}

	now := time.Now()
	stream.lastAccessed = now
	if segment != -1 {
		stream.lastSegment = segment
	}

	waitingSegment := &waitingSegment{
		segmentType: streamType.SegmentType,
		idx:         segment,
		file:        file,
		path:        filepath.Join(sm.cacheDir, file),
		accessed:    now,
		available:   make(chan error, 1),
	}
	stream.waitingSegments = append(stream.waitingSegments, waitingSegment)

	sm.streamsMutex.Unlock()

	sm.serveWaitingSegment(w, r, waitingSegment)
}

// assume lock is held
func (sm *StreamManager) startTranscode(stream *runningStream, segment int, done chan<- error) {
	// generate segment 0 if init segment requested
	if segment == -1 {
		segment = 0
	}

	logger.Debugf("[transcode] starting transcode for %s at segment #%d", stream.dir, segment)

	if err := os.MkdirAll(stream.outputDir, os.ModePerm); err != nil {
		logger.Errorf("[transcode] %v", err)
		done <- err
		return
	}

	lockCtx := sm.lockManager.ReadLock(sm.context, stream.vf.Path)

	args := stream.makeStreamArgs(sm, segment)
	cmd := sm.encoder.Command(lockCtx, args)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		logger.Errorf("[transcode] ffmpeg stderr not available: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if nil != err {
		logger.Errorf("[transcode] ffmpeg stdout not available: %v", err)
	}

	logger.Tracef("[transcode] running %s", cmd)
	if err := cmd.Start(); err != nil {
		lockCtx.Cancel()
		err = fmt.Errorf("error starting transcode process: %w", err)
		logger.Errorf("[transcode] %v", err)
		done <- err
		return
	}

	tp := &transcodeProcess{
		cmd:         cmd,
		context:     lockCtx,
		cancel:      lockCtx.Cancel,
		outputDir:   stream.outputDir,
		segmentType: stream.streamType.SegmentType,
		segment:     segment,
	}
	stream.tp = tp

	go func() {
		errStr, _ := io.ReadAll(stderr)
		outStr, _ := io.ReadAll(stdout)

		errCmd := cmd.Wait()

		var err error

		// don't log error if cancelled
		if !tp.cancelled {
			e := string(errStr)
			if e == "" {
				e = string(outStr)
			}
			if e != "" {
				err = errors.New(e)
			} else {
				err = errCmd
			}

			if err != nil {
				err = fmt.Errorf("ffmpeg error when running command <%s>: %w", strings.Join(cmd.Args, " "), err)

				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					logger.Errorf("[transcode] %v", err)
				}
			}
		}

		sm.streamsMutex.Lock()

		// make sure that cancel is called to prevent memory leaks
		tp.cancel()

		// clear remaining segments after ffmpeg exit
		tp.checkSegments()

		if stream.tp == tp {
			stream.tp = nil
		}

		sm.streamsMutex.Unlock()

		if err != nil {
			done <- err
		}
	}()
}

// assume lock is held
func (sm *StreamManager) stopTranscode(stream *runningStream) {
	tp := stream.tp
	if tp != nil {
		tp.cancel()
		tp.cancelled = true
	}
}

func (sm *StreamManager) checkTranscode(stream *runningStream, now time.Time) {
	if len(stream.waitingSegments) == 0 && stream.lastAccessed.Add(maxIdleTime).Before(now) {
		// Stream expired. Cancel the transcode process and delete the files
		logger.Debugf("[transcode] stream for %s not accessed recently. Cancelling transcode and removing files", stream.dir)

		sm.stopTranscode(stream)
		sm.removeTranscodeFiles(stream)

		delete(sm.runningStreams, stream.dir)
		return
	}

	if stream.tp != nil {
		segmentType := stream.streamType.SegmentType
		segment := stream.lastSegment
		// if all segments up to maxSegmentBuffer exist, stop transcode
		for i := segment; i < segment+maxSegmentBuffer; i++ {
			if !segmentExists(filepath.Join(stream.outputDir, segmentType.MakeFilename(i))) {
				return
			}
		}

		logger.Debugf("[transcode] stopping transcode for %s, buffer is full", stream.dir)
		sm.stopTranscode(stream)
	}
}

func (s *waitingSegment) checkAvailable(now time.Time) bool {
	if segmentExists(s.path) {
		s.available <- nil
		return true
	} else if s.accessed.Add(maxSegmentWait).Before(now) {
		err := fmt.Errorf("timed out waiting for segment file %s to be generated", s.file)
		logger.Errorf("[transcode] %v", err)
		s.available <- err
		return true
	}
	return false
}

// ensureTranscode will start a new transcode process if the transcode
// is more than maxSegmentGap behind the requested segment
func (sm *StreamManager) ensureTranscode(stream *runningStream, segment *waitingSegment) bool {
	segmentIdx := segment.idx
	tp := stream.tp
	if tp == nil {
		sm.startTranscode(stream, segmentIdx, segment.available)
		return true
	} else if segmentIdx < tp.segment || tp.segment+maxSegmentGap < segmentIdx {
		// only stop the transcode process here - it will be restarted only
		// after the old process exits as stream.tp will then be nil.
		sm.stopTranscode(stream)
		return true
	}
	return false
}

// runs every monitorInterval
func (sm *StreamManager) monitorStreams() {
	sm.streamsMutex.Lock()
	defer sm.streamsMutex.Unlock()

	now := time.Now()

	for _, stream := range sm.runningStreams {
		if stream.tp != nil {
			stream.tp.checkSegments()
		}

		transcodeStarted := false
		temp := stream.waitingSegments[:0]
		for _, segment := range stream.waitingSegments {
			remove := false
			if segment.done.Load() || segment.checkAvailable(now) {
				remove = true
			} else if !transcodeStarted {
				transcodeStarted = sm.ensureTranscode(stream, segment)
			}
			if !remove {
				temp = append(temp, segment)
			}
		}
		stream.waitingSegments = temp

		if !transcodeStarted {
			sm.checkTranscode(stream, now)
		}
	}
}

// assume lock is held
func (sm *StreamManager) removeTranscodeFiles(stream *runningStream) {
	path := stream.outputDir
	if err := os.RemoveAll(path); err != nil {
		logger.Warnf("[transcode] error removing segment directory %s: %v", path, err)
	}
}

// stopAndRemoveAll stops all current streams and removes all cache files
func (sm *StreamManager) stopAndRemoveAll() {
	sm.streamsMutex.Lock()
	defer sm.streamsMutex.Unlock()

	for _, stream := range sm.runningStreams {
		for _, segment := range stream.waitingSegments {
			if len(segment.available) == 0 {
				segment.available <- context.Canceled
			}
		}
		sm.stopTranscode(stream)
		sm.removeTranscodeFiles(stream)
	}

	// ensure nothing else can use the map
	sm.runningStreams = nil
}
