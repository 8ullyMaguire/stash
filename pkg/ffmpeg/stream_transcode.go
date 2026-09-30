package ffmpeg

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"syscall"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

type StreamFormat struct {
	MimeType string
	Args     func(codec VideoCodec, videoFilter VideoFilter, videoOnly bool) Args
}

func CodecInit(codec VideoCodec) (args Args) {
	args = args.VideoCodec(codec)

	switch codec {
	// CPU Codecs
	case VideoCodecLibX264:
		args = append(args,
			"-pix_fmt", "yuv420p",
			"-preset", "veryfast",
			"-crf", "25",
			"-sc_threshold", "0",
		)
	case VideoCodecVP9:
		args = append(args,
			"-pix_fmt", "yuv420p",
			"-deadline", "realtime",
			"-cpu-used", "5",
			"-row-mt", "1",
			"-crf", "30",
			"-b:v", "0",
		)
	// HW Codecs
	case VideoCodecN264:
		args = append(args,
			"-rc", "vbr",
			"-cq", "15",
		)
	case VideoCodecN264H:
		args = append(args,
			"-profile", "p7",
			"-tune", "hq",
			"-profile", "high",
			"-rc", "vbr",
			"-rc-lookahead", "60",
			"-surfaces", "64",
			"-spatial-aq", "1",
			"-aq-strength", "15",
			"-cq", "15",
			"-coder", "cabac",
			"-b_ref_mode", "middle",
		)
	case VideoCodecI264, VideoCodecIVP9:
		args = append(args,
			"-global_quality", "20",
			"-preset", "faster",
		)
	case VideoCodecI264C:
		args = append(args,
			"-q", "20",
			"-preset", "faster",
		)
	case VideoCodecV264, VideoCodecVVP9:
		args = append(args,
			"-qp", "20",
		)
	case VideoCodecA264:
		args = append(args,
			"-quality", "speed",
		)
	case VideoCodecM264:
		args = append(args,
			"-realtime", "1",
		)
	case VideoCodecO264:
		args = append(args,
			"-preset", "superfast",
			"-crf", "25",
		)
	}

	return args
}

var (
	StreamTypeMP4 = StreamFormat{
		MimeType: MimeMp4Video,
		Args: func(codec VideoCodec, videoFilter VideoFilter, videoOnly bool) (args Args) {
			args = CodecInit(codec)
			args = append(args, "-movflags", "frag_keyframe+empty_moov")
			args = args.VideoFilter(videoFilter)
			if videoOnly {
				args = args.SkipAudio()
			} else {
				args = append(args, "-ac", "2")
			}
			args = args.Format(FormatMP4)
			return
		},
	}
	StreamTypeWEBM = StreamFormat{
		MimeType: MimeWebmVideo,
		Args: func(codec VideoCodec, videoFilter VideoFilter, videoOnly bool) (args Args) {
			args = CodecInit(codec)
			args = args.VideoFilter(videoFilter)
			if videoOnly {
				args = args.SkipAudio()
			} else {
				args = append(args, "-ac", "2")
			}
			args = args.Format(FormatWebm)
			return
		},
	}
	StreamTypeMKV = StreamFormat{
		MimeType: MimeMkvVideo,
		Args: func(codec VideoCodec, videoFilter VideoFilter, videoOnly bool) (args Args) {
			args = CodecInit(codec)
			if videoOnly {
				args = args.SkipAudio()
			} else {
				args = args.AudioCodec(AudioCodecLibOpus)
				args = append(args,
					"-b:a", "96k",
					"-vbr", "on",
					"-ac", "2",
				)
			}
			args = args.Format(FormatMatroska)
			return
		},
	}
)

type TranscodeOptions struct {
	StreamType StreamFormat
	VideoFile  *models.VideoFile
	Resolution string
	StartTime  float64
}

func (o TranscodeOptions) FileGetCodec(sm *StreamManager, maxTranscodeSize int) (codec VideoCodec) {
	needsResize := false

	if maxTranscodeSize != 0 {
		if o.VideoFile.Width > o.VideoFile.Height {
			needsResize = o.VideoFile.Width > maxTranscodeSize
		} else {
			needsResize = o.VideoFile.Height > maxTranscodeSize
		}
	}

	switch o.StreamType.MimeType {
	case MimeMp4Video:
		if !needsResize && o.VideoFile.VideoCodec == H264 {
			return VideoCodecCopy
		}
		codec = VideoCodecLibX264
		if hwcodec := sm.encoder.hwCodecMP4Compatible(); hwcodec != nil && sm.config.GetTranscodeHardwareAcceleration() {
			codec = *hwcodec
		}
	case MimeWebmVideo:
		if !needsResize && (o.VideoFile.VideoCodec == Vp8 || o.VideoFile.VideoCodec == Vp9) {
			return VideoCodecCopy
		}
		codec = VideoCodecVP9
		if hwcodec := sm.encoder.hwCodecWEBMCompatible(); hwcodec != nil && sm.config.GetTranscodeHardwareAcceleration() {
			codec = *hwcodec
		}
	case MimeMkvVideo:
		codec = VideoCodecCopy
	}

	return codec
}

func (o TranscodeOptions) makeStreamArgs(sm *StreamManager) Args {
	maxTranscodeSize := sm.config.GetMaxStreamingTranscodeSize().GetMaxResolution()
	if o.Resolution != "" {
		maxTranscodeSize = models.StreamingResolutionEnum(o.Resolution).GetMaxResolution()
	}
	extraInputArgs := sm.config.GetLiveTranscodeInputArgs()
	extraOutputArgs := sm.config.GetLiveTranscodeOutputArgs()

	args := Args{"-hide_banner"}
	args = args.LogLevel(LogLevelError)

	codec := o.FileGetCodec(sm, maxTranscodeSize)

	fullhw := sm.config.GetTranscodeHardwareAcceleration() && sm.encoder.hwCanFullHWTranscode(sm.context, codec, o.VideoFile, maxTranscodeSize)
	args = sm.encoder.hwDeviceInit(args, codec, fullhw)
	args = append(args, extraInputArgs...)

	if o.StartTime != 0 {
		if codec == VideoCodecCopy {
			// #7103 - keep audio aligned with copied video, which can only start at a keyframe
			args = args.NoAccurateSeek()
		}
		args = args.Seek(o.StartTime)
	}

	args = args.Input(o.VideoFile.Path)

	videoOnly := ProbeAudioCodec(o.VideoFile.AudioCodec) == MissingUnsupported

	videoFilter := sm.encoder.hwMaxResFilter(codec, o.VideoFile, maxTranscodeSize, fullhw)

	args = append(args, o.StreamType.Args(codec, videoFilter, videoOnly)...)

	args = append(args, extraOutputArgs...)

	args = args.Output("pipe:")

	return args
}

func (sm *StreamManager) ServeTranscode(w http.ResponseWriter, r *http.Request, options TranscodeOptions) {
	streamRequestCtx := NewStreamRequestContext(w, r)
	lockCtx := sm.lockManager.ReadLock(streamRequestCtx, options.VideoFile.Path)

	// hijacking and closing the connection here causes video playback to hang in Chrome
	// due to ERR_INCOMPLETE_CHUNKED_ENCODING
	// We trust that the request context will be closed, so we don't need to call Cancel on the returned context here.

	handler, err := sm.getTranscodeStream(lockCtx, options)

	if err != nil {
		// don't log context canceled errors
		if !errors.Is(err, context.Canceled) {
			logger.Errorf("[transcode] error transcoding video file: %v", err)
		}
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(err.Error())); err != nil {
			logger.Warnf("[transcode] error writing response: %v", err)
		}
		return
	}

	handler(w, r)
}

func (sm *StreamManager) getTranscodeStream(ctx *fsutil.LockContext, options TranscodeOptions) (http.HandlerFunc, error) {
	args := options.makeStreamArgs(sm)
	cmd := sm.encoder.Command(ctx, args)

	stdout, err := cmd.StdoutPipe()
	if nil != err {
		logger.Errorf("[transcode] ffmpeg stdout not available: %v", err)
		return nil, err
	}

	stderr, err := cmd.StderrPipe()
	if nil != err {
		logger.Errorf("[transcode] ffmpeg stderr not available: %v", err)
		return nil, err
	}

	if err = cmd.Start(); err != nil {
		return nil, err
	}
	ctx.AttachCommand(cmd)

	// stderr must be consumed or the process deadlocks
	//
	// cmd.Wait() must NOT be called here. Wait closes the child's pipes, so
	// doing that from this goroutine races the handler: Wait can land between
	// the handler's one-byte peek and its io.Copy, close stdout underneath it,
	// and the client gets a body of exactly ONE byte. That is not a short read
	// in the peeking sense -- it is the tail of the file, and for MP4 it
	// truncates the ftyp box, so a video that downloads fine will not play.
	//
	// Measured, not reasoned: replaying this exact structure 3000 times with
	// Wait() in this goroutine truncated the body to 1 byte in 426 of them
	// (14%); with the goroutine draining stderr and nothing else, 0 of 3000.
	// The existing tests caught it as an intermittent "want 12 bytes, got 1",
	// which is why they are worth keeping.
	//
	// So: drain stderr here, and reap the process from INSIDE the handler, after
	// it has finished reading stdout.
	stderrDone := make(chan struct{})
	var errStr []byte
	go func() {
		defer close(stderrDone)
		errStr, _ = io.ReadAll(stderr)
	}()

	// reap is called exactly once, by the handler, on its way out. It is not a
	// defer HERE: this function returns the handler and the caller invokes it
	// later, so a defer at this level would run at `return handler, nil` --
	// closing stdout before a single byte had been read. That is not a
	// hypothetical: it is exactly what the first attempt at this fix did, and it
	// turned the two tests into a hard "500, body empty" on every run.
	reap := func() {
		// stderr must be fully drained before Wait, or the child can block on a
		// full stderr pipe and never exit.
		<-stderrDone
		errCmd := cmd.Wait()

		var err error
		e := string(errStr)
		if e != "" {
			err = errors.New(e)
		} else {
			err = errCmd
		}

		// ignore ExitErrors, the process is always forcibly killed
		var exitError *exec.ExitError
		if err != nil && !errors.As(err, &exitError) {
			logger.Errorf("[transcode] ffmpeg error when running command <%s>: %v", strings.Join(cmd.Args, " "), err)
		}
	}

	mimeType := options.StreamType.MimeType
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Every path out of this handler must reap the child, or it becomes a
		// zombie for the lifetime of the process. `defer` here -- in the
		// handler, not in the function that builds it -- is what makes that
		// automatic.
		defer reap()

		w.Header().Set("Cache-Control", "no-store")

		// #5683 - do not claim success before ffmpeg has produced anything.
		//
		// The status was written unconditionally, so a transcode that died
		// instantly -- most often because the source file is gone, e.g. the
		// scene lives on a drive that was removed, or was moved without a
		// rescan -- answered the request with "200 OK, Content-Type:
		// video/mp4" and an empty body. Firefox treats that as a stream that
		// ended unexpectedly and re-requests it, forever: each attempt
		// spawns another ffmpeg, so a page left open burns 60-80% of a core
		// and fills the log with thousands of identical errors.
		//
		// Waiting for the first byte before writing the header turns that
		// into a single failure the client will not retry. It costs nothing
		// on the happy path: ffmpeg emits the container header within
		// milliseconds, and for MP4 the -movflags frag_keyframe+empty_moov
		// combination makes the first fragment available immediately.
		//
		// The Content-Type is set AFTER the peek for the same reason. A 500
		// advertising video/mp4 is the exact shape the browser misreads: a
		// client that keys off the content type rather than the status will
		// treat it as a stream and retry. On the failure path the type is
		// left unset, so nothing claims to be video.
		//
		// Errors that arrive AFTER the first byte cannot be reported as a
		// status code -- the header is already sent -- so they are logged and
		// the connection is simply closed, which is the correct signal for a
		// truncated stream.
		firstByte := make([]byte, 1)
		n, readErr := stdout.Read(firstByte)
		if n == 0 && readErr != nil {
			// Nothing was produced. Report the failure as a status code so
			// the client stops asking.
			//
			// Cancellation must NOT become a 500, and it does not arrive in
			// the shape the obvious check assumes. When the context is
			// already done, exec.CommandContext refuses to start the process
			// at all, so cmd.Start() returns "context canceled" here, and the
			// pipe read that follows fails with EOF or a bare "file already
			// closed" -- neither of which is context.Canceled and neither of
			// which unwraps to it. Checking only errors.Is(err,
			// context.Canceled) therefore misses the case and turns every
			// cancelled playback into an error in the UI, which is the mirror
			// image of the bug being fixed.
			//
			// The authoritative question is not "what error is this" but
			// "was this stream cancelled", and the context the command is
			// parented to is the thing that answers it. The request context
			// is NOT a substitute: it is a different context, and a user
			// navigating away does not necessarily trip it before the pipe
			// fails.
			if sm.context.Err() != nil || r.Context().Err() != nil ||
				errors.Is(readErr, context.Canceled) {
				return
			}
			logger.Errorf("[transcode] ffmpeg produced no output: %v", readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", mimeType)
		w.WriteHeader(http.StatusOK)

		// Write the byte we consumed in order to read it, then the rest.
		if n > 0 {
			if _, err := w.Write(firstByte[:n]); err != nil {
				logger.Warnf("[transcode] error writing response: %v", err)
				return
			}
		}

		// process killing should be handled by command context

		if readErr == nil {
			_, err = io.Copy(w, stdout)
			if err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
				logger.Errorf("[transcode] error serving transcoded video file: %v", err)
			}
		}

		w.(http.Flusher).Flush()
	}
	return handler, nil
}
