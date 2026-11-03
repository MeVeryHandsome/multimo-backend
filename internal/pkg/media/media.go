// Package media wraps ffprobe/ffmpeg: video metadata, single-frame
// extraction and thumbnails.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ProbeInfo struct {
	FormatName   string            `json:"formatName"`
	Duration     float64           `json:"duration"`
	SizeBytes    int64             `json:"sizeBytes"`
	BitRate      int64             `json:"bitRate"`
	VideoCodec   string            `json:"videoCodec"`
	AudioCodec   string            `json:"audioCodec,omitempty"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	FPS          float64           `json:"fps"`
	Frames       int               `json:"frames"`
	Rotation     int               `json:"rotation,omitempty"`
	CreationTime *time.Time        `json:"creationTime,omitempty"`
	Lat          *float64          `json:"lat,omitempty"`
	Lon          *float64          `json:"lon,omitempty"`
	Alt          *float64          `json:"alt,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

type probeWire struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		Size       string            `json:"size"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		AvgFrameRate string            `json:"avg_frame_rate"`
		RFrameRate   string            `json:"r_frame_rate"`
		NbFrames     string            `json:"nb_frames"`
		Duration     string            `json:"duration"`
		Tags         map[string]string `json:"tags"`
		SideData     []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
}

// Probe reads container and stream metadata with ffprobe.
func Probe(ctx context.Context, path string) (*ProbeInfo, error) {
	out, err := run(ctx, "ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return nil, err
	}
	var w probeWire
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	p := &ProbeInfo{FormatName: w.Format.FormatName, Tags: map[string]string{}}
	p.Duration, _ = strconv.ParseFloat(w.Format.Duration, 64)
	p.SizeBytes, _ = strconv.ParseInt(w.Format.Size, 10, 64)
	p.BitRate, _ = strconv.ParseInt(w.Format.BitRate, 10, 64)
	for k, v := range w.Format.Tags {
		p.Tags[strings.ToLower(k)] = v
	}
	for _, s := range w.Streams {
		switch s.CodecType {
		case "video":
			if p.VideoCodec != "" {
				continue
			}
			p.VideoCodec, p.Width, p.Height = s.CodecName, s.Width, s.Height
			p.FPS = parseRate(s.AvgFrameRate)
			if p.FPS == 0 {
				p.FPS = parseRate(s.RFrameRate)
			}
			p.Frames, _ = strconv.Atoi(s.NbFrames)
			if p.Duration == 0 {
				p.Duration, _ = strconv.ParseFloat(s.Duration, 64)
			}
			for k, v := range s.Tags {
				if _, ok := p.Tags[strings.ToLower(k)]; !ok {
					p.Tags[strings.ToLower(k)] = v
				}
			}
			for _, sd := range s.SideData {
				if sd.Rotation != 0 {
					p.Rotation = int(sd.Rotation)
				}
			}
			if r, err := strconv.Atoi(s.Tags["rotate"]); err == nil {
				p.Rotation = r
			}
		case "audio":
			if p.AudioCodec == "" {
				p.AudioCodec = s.CodecName
			}
		}
	}
	if p.Frames == 0 && p.FPS > 0 {
		p.Frames = int(p.Duration*p.FPS + 0.5)
	}
	if t, err := time.Parse(time.RFC3339Nano, p.Tags["creation_time"]); err == nil && t.Year() > 1971 {
		p.CreationTime = &t
	}
	for _, k := range []string{"location", "com.apple.quicktime.location.iso6709", "location-eng"} {
		if lat, lon, alt, ok := ParseISO6709(p.Tags[k]); ok {
			p.Lat, p.Lon, p.Alt = &lat, &lon, alt
			break
		}
	}
	if p.Width == 0 {
		return p, fmt.Errorf("no video stream in %s", filepath.Base(path))
	}
	return p, nil
}

var iso6709 = regexp.MustCompile(`^([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)?`)

// ParseISO6709 parses the decimal-degree form used in MP4/MOV location
// tags, e.g. "+33.7332-118.3347+012.000/".
func ParseISO6709(s string) (lat, lon float64, alt *float64, ok bool) {
	m := iso6709.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, nil, false
	}
	lat, _ = strconv.ParseFloat(m[1], 64)
	lon, _ = strconv.ParseFloat(m[2], 64)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, nil, false
	}
	if m[3] != "" {
		a, _ := strconv.ParseFloat(m[3], 64)
		alt = &a
	}
	return lat, lon, alt, true
}

var fileStamp = regexp.MustCompile(`(20\d{2})(0[1-9]|1[0-2])([0-2]\d|3[01])[_-]?([01]\d|2[0-3])([0-5]\d)([0-5]\d)`)

// FilenameTime extracts a YYYYMMDD[_]HHMMSS stamp, as written by DJI and most
// cameras. The clock is the device's local time; the zone is unknown, so the
// result is returned as UTC wall time and should be labelled as such.
func FilenameTime(name string) (time.Time, bool) {
	m := fileStamp.FindStringSubmatch(filepath.Base(name))
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102150405", m[1]+m[2]+m[3]+m[4]+m[5]+m[6])
	return t, err == nil
}

// ExtractFrame writes the frame at ts seconds as JPEG, scaled down to at most
// maxWidth pixels wide (aspect kept, even height).
func ExtractFrame(ctx context.Context, src string, ts float64, dst string, maxWidth, threads int) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp.jpg"
	_, err := run(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(ts, 'f', 3, 64), "-i", src, "-frames:v", "1",
		"-vf", scaleFilter(maxWidth), "-q:v", "3", "-threads", strconv.Itoa(max(threads, 1)), tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ImageThumb converts an image ffmpeg can decode (JPEG, PNG, simple TIFF) to a
// JPEG thumbnail no wider than maxWidth.
func ImageThumb(ctx context.Context, src, dst string, maxWidth int) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp.jpg"
	_, err := run(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-i", src,
		"-frames:v", "1", "-vf", scaleFilter(maxWidth), "-q:v", "3", tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func scaleFilter(maxWidth int) string {
	return fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth)
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, _ := strconv.ParseFloat(num, 64)
	if !ok {
		return n
	}
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return stdout.Bytes(), nil
}

// Scene is a frame where ffmpeg's scene-change score passed the threshold.
type Scene struct {
	Time  float64 `json:"time"`
	Score float64 `json:"score"`
}

// SceneChanges runs ffmpeg's scene detector on a 320 px wide copy of the
// stream (cheap to decode) and returns frames scoring above threshold (0..1).
func SceneChanges(ctx context.Context, path string, threshold float64, threads int) ([]Scene, error) {
	filter := fmt.Sprintf("scale=320:-2,select='gt(scene,%.3f)',metadata=print:file=-", threshold)
	out, err := run(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-threads", strconv.Itoa(max(threads, 1)),
		"-i", path, "-an", "-vf", filter, "-f", "null", "-")
	if err != nil {
		return nil, err
	}
	return parseSceneMetadata(string(out)), nil
}

// parseSceneMetadata reads ffmpeg metadata=print output:
//
//	frame:12   pts:12288   pts_time:0.48
//	lavfi.scene_score=0.512
func parseSceneMetadata(out string) []Scene {
	var scenes []Scene
	cur := -1.0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "pts_time:"); i >= 0 {
			f := strings.Fields(line[i+len("pts_time:"):])
			if len(f) > 0 {
				cur, _ = strconv.ParseFloat(f[0], 64)
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "lavfi.scene_score="); ok && cur >= 0 {
			s, _ := strconv.ParseFloat(v, 64)
			scenes = append(scenes, Scene{Time: cur, Score: s})
			cur = -1
		}
	}
	return scenes
}

// Signature is a tiny greyscale copy of one frame, for change detection.
type Signature struct {
	Time float64
	Pix  []byte // SigW x SigH, row major
}

const SigW, SigH = 64, 36

// FrameSignatures decodes the video once at fps frames per second into
// 64x36 greyscale signatures. keyOnly decodes only key frames (about 3x
// faster on long 1080p footage); between key frames the last one repeats.
func FrameSignatures(ctx context.Context, path string, fps float64, threads int, keyOnly bool) ([]Signature, error) {
	vf := fmt.Sprintf("fps=%g,scale=%d:%d,format=gray", fps, SigW, SigH)
	args := []string{"-nostdin", "-loglevel", "error", "-threads", strconv.Itoa(max(threads, 1))}
	if keyOnly {
		args = append(args, "-skip_frame", "nokey")
	}
	args = append(args, "-i", path, "-an", "-vf", vf, "-f", "rawvideo", "-")
	out, err := run(ctx, "ffmpeg", args...)
	if err != nil {
		return nil, err
	}
	n := len(out) / (SigW * SigH)
	sigs := make([]Signature, n)
	for i := 0; i < n; i++ {
		sigs[i] = Signature{Time: float64(i) / fps, Pix: out[i*SigW*SigH : (i+1)*SigW*SigH]}
	}
	return sigs, nil
}

// Change is the brightness-normalised mean absolute difference of two
// signatures, in 0..1.
func Change(a, b []byte) float64 {
	var ma, mb float64
	for i := range a {
		ma += float64(a[i])
		mb += float64(b[i])
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	var d float64
	for i := range a {
		x := (float64(a[i]) - ma) - (float64(b[i]) - mb)
		if x < 0 {
			x = -x
		}
		d += x
	}
	return d / float64(len(a)) / 255
}

// Distinct counts signatures that differ from their predecessor.
func Distinct(sigs []Signature) int {
	n := 0
	for i := range sigs {
		if i == 0 || string(sigs[i].Pix) != string(sigs[i-1].Pix) {
			n++
		}
	}
	return n
}
