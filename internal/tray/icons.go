package tray

import "math"

// Pixmap is one rendered icon frame in the D-Bus wire format: ARGB32 with
// each pixel stored as the four bytes A,R,G,B, row-major, big-endian
// ("network byte order" in the StatusNotifierItem spec). It maps to one
// element of the a(iiay) IconPixmap property.
type Pixmap struct {
	Width  int
	Height int
	ARGB   []byte // Width*Height*4 bytes
}

// Icon is the pure result of IconForState — a semantic name plus the
// rendered pixmaps. Being a plain value makes the whole state→icon mapping
// unit-testable without a D-Bus connection.
type Icon struct {
	// Name is the semantic icon id from the spec (§6):
	// "idle", "rec", "transcribe", "speaking" or "error".
	Name string
	// Pixmaps carries several sizes so the tray host can pick the nearest
	// one instead of scaling a single 22px square up to a retina panel.
	Pixmaps []Pixmap
}

// stateColor is the dot color per daemon state. Colors are picked to read
// clearly on both light and dark panels: nothing too pale, nothing neon.
var stateColor = map[State][3]uint8{
	StateIdle:         {0x9E, 0x9E, 0x9E}, // grey — nothing happening
	StateRecording:    {0xE5, 0x39, 0x35}, // red — microphone is live
	StateTranscribing: {0xFD, 0xD8, 0x35}, // yellow — waiting on the provider
	StateSpeaking:     {0x43, 0xA0, 0x47}, // green — audio out
	StateError:        {0xFB, 0x8C, 0x00}, // orange — needs a look
}

// stateIconName keeps the spec's short icon names per state.
var stateIconName = map[State]string{
	StateIdle:         "idle",
	StateRecording:    "rec",
	StateTranscribing: "transcribe",
	StateSpeaking:     "speaking",
	StateError:        "error",
}

// iconSizes are the edge lengths we render. 22/24 cover classic panels,
// 32/48 cover HiDPI without the host having to upscale.
var iconSizes = []int{22, 24, 32, 48}

// IconForState maps a daemon state to its tray icon. Unknown states fall
// back to the grey idle dot: an icon that cannot identify its state is far
// less confusing than a blank square in the panel.
//
// The icon is rendered on the spot — flat circle, anti-aliased edge, thin
// darker ring for contrast. We deliberately send pixels (IconPixmap) and
// never an IconName: a theme lookup for "hey-agent" finds nothing because
// the daemon installs no .desktop/icon files, while a pixmap renders on
// every host.
func IconForState(s State) Icon {
	name, ok := stateIconName[s]
	if !ok {
		s = StateIdle
		name = stateIconName[s]
	}
	color := stateColor[s]
	pixmaps := make([]Pixmap, 0, len(iconSizes))
	for _, size := range iconSizes {
		pixmaps = append(pixmaps, circlePixmap(size, color[0], color[1], color[2]))
	}
	return Icon{Name: name, Pixmaps: pixmaps}
}

// statusForState maps a daemon state to the SNI Status property. "Active"
// means "show me"; "Passive" would hide the icon entirely and "NeedsAttention"
// makes hosts flash/highlight it — reserved for errors only.
func statusForState(s State) string {
	if s == StateError {
		return "NeedsAttention"
	}
	return "Active"
}

// circlePixmap renders one size of the state dot.
func circlePixmap(size int, r, g, b uint8) Pixmap {
	// Leave ~1px of padding so the anti-aliased edge is clipped by the
	// pixmap bounds, not by the panel.
	radius := float64(size)/2 - 1
	center := float64(size) / 2
	data := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Sample the pixel center: an integer x,y offset by half a pixel
			// gives a symmetric circle on even sizes.
			d := math.Hypot(float64(x)+0.5-center, float64(y)+0.5-center)
			alpha := clamp01(radius - d)

			pr, pg, pb := float64(r), float64(g), float64(b)
			if d > radius-1.5 {
				// Outermost ring drawn darker — a flat pale circle melts
				// into light panels without an edge.
				pr, pg, pb = pr*0.55, pg*0.55, pb*0.55
			}
			i := (y*size + x) * 4
			// Wire order is A,R,G,B. RGB is written alpha-premultiplied:
			// that is what Go's color.RGBA() produces and what the hosts
			// that composite these pixmaps expect.
			data[i] = uint8(alpha * 255)
			data[i+1] = uint8(pr * alpha)
			data[i+2] = uint8(pg * alpha)
			data[i+3] = uint8(pb * alpha)
		}
	}
	return Pixmap{Width: size, Height: size, ARGB: data}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// sniPixmap is Pixmap reshaped for marshalling: the a(iiay) signature needs
// int32 dimensions and a byte array, and godbus turns a Go struct into a
// D-Bus struct field-by-field.
type sniPixmap struct {
	Width  int32
	Height int32
	Data   []byte
}

// sniPixmaps converts an Icon to the IconPixmap property value.
func (i Icon) sniPixmaps() []sniPixmap {
	out := make([]sniPixmap, 0, len(i.Pixmaps))
	for _, p := range i.Pixmaps {
		out = append(out, sniPixmap{
			Width:  int32(p.Width),
			Height: int32(p.Height),
			Data:   p.ARGB,
		})
	}
	return out
}

// sniToolTip is the (sa(iiay)ss) ToolTip property: icon name, pixmap list,
// title line, subtitle line.
type sniToolTip struct {
	IconName string
	Pixmaps  []sniPixmap
	Title    string
	SubTitle string
}
