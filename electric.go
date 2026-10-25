package main

import (
	_ "embed"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// Fast mode's electricity, after the charged border of Rune's effects
// (github.com/anomalyco/rune, crates/rune-fx), its calm one for UI turned
// up a little: a charged hairline around the model button that wanders as
// the current crawls along it, brighter here and dimmer there, beads of
// charge running around it, and sparks that shoot out and cool from white
// to violet. Next to Rune's, the line strays further and moves faster,
// and sparks fly about three times as often. The line and its sparks are
// worked out here each frame, and electric.kage lights the pixels around
// them.

//go:embed electric.kage
var electricSource string

var (
	electricShader = shader.MustCompile("package main\n" + electricSource)
	elInset        = electricShader.Uniform("Inset")
	elRadius       = electricShader.Uniform("Radius")
	elGap          = electricShader.Uniform("Gap")
	elWidth        = electricShader.Uniform("Width")
	elGlow         = electricShader.Uniform("Glow")
	elCount        = electricShader.Uniform("Count")
	elPoints       = electricShader.Uniform("Points")
	elBeads        = electricShader.Uniform("Beads")
	elIntensity    = electricShader.Uniform("Intensity")
	elInk          = electricShader.Uniform("Ink")
	elSparkAt      = electricShader.Uniform("SparkAt")
	elSparkLife    = electricShader.Uniform("SparkLife")
	elSparkWalk    = electricShader.Uniform("SparkWalk")
	elTint         = electricShader.Uniform("Tint")
	elHot          = electricShader.Uniform("Hot")
	elCool         = electricShader.Uniform("Cool")
	electricValues = electricShader.NewValues()
	// electricStart is when the electricity's clock starts, for fast mode
	// on from the start.
	electricStart = time.Now()
)

// The electricity's look, in DIPs and seconds: how far the line strays
// from the outline, how many waves it has (a 100 DIPs), and how far out
// of it it runs; its core and its sheath; how fast the current moves, how
// often a spark flies (0 to 1) and how far, and how lit all of it is.
const (
	elAmplitude  = 1.6
	elFrequency  = 3.0
	elOut        = 0.5
	elCore       = 0.5
	elSheath     = 4.0
	elSpeed      = 3.5
	elSparkRate  = 0.7
	elSparkLen   = 18.0 * 0.4
	elBrightness = 0.9
	// elMargin is how far past the outline the electricity can reach.
	elMargin = 14
)

// boltCool is the color a spark cools to.
var boltCool = ui.Hex("#7a4dff")

const (
	elMaxPoints = 128
	elSparks    = 5
	elWalk      = 8 // points along a spark
)

// electricity is a frame of the charged line around an outline: what
// electric.kage reads.
type electricity struct {
	count              int
	points             [elMaxPoints * 4]float32
	beads              float32
	sparkAt, sparkLife [elSparks * 4]float32
	sparkWalk          [elSparks * elWalk]float32
}

// gauss3 is about a gaussian, from -3 to 3, that only depends on its
// arguments.
func gauss3(a, b, c int) float32 {
	return (hash(a, b, c) + hash(a, b, c+100) + hash(a, b, c+200) - 1.5) * 2
}

// noise is smooth gradient noise, about -1 to 1, over (x, y), repeating
// every per in x unless per is 0.
func noise(x, y float64, per, seed int) float32 {
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := float32(x-math.Floor(x)), float32(y-math.Floor(y))
	x0, x1 := ix, ix+1
	if per > 0 {
		x0 = (ix%per + per) % per
		x1 = (x0 + 1) % per
	}
	dot := func(cx, cy int, dx, dy float32) float32 {
		sin, cos := math.Sincos(float64(hash(cx, cy, seed)) * 2 * math.Pi)
		return float32(cos)*dx + float32(sin)*dy
	}
	fade := func(f float32) float32 { return f * f * f * (f*(f*6-15) + 10) }
	ux, uy := fade(fx), fade(fy)
	lo := dot(x0, iy, fx, fy) + (dot(x1, iy, fx-1, fy)-dot(x0, iy, fx, fy))*ux
	hi := dot(x0, iy+1, fx, fy-1) + (dot(x1, iy+1, fx-1, fy-1)-dot(x0, iy+1, fx, fy-1))*ux
	return (lo + (hi-lo)*uy) * 1.41421
}

// wander is how far the line strays at x of a loop of cells, t: three
// octaves of noise, each twice as fine, half as far and changing faster.
func wander(x, t float64, cells int) float32 {
	var sum, norm float32
	a := float32(1)
	for o := range 3 {
		sum += a * noise(x, t, cells, 60+o*101)
		norm += a
		x, t, cells, a = x*2, t*1.55, cells*2, a/2
	}
	return sum / norm * 1.6
}

// frame works out the line around an outline total DIPs around, t seconds
// in, or, still, a pose that holds.
func (e *electricity) frame(total float32, t float64, still bool) {
	if still {
		t = 0
	}
	n := elMaxPoints
	e.count = n
	cells := max(int(total*elFrequency/100+0.5), 3)
	sp := elSpeed * 0.35
	var line [elMaxPoints]float32
	for i := range n {
		line[i] = wander(float64(i)/float64(n)*float64(cells), t*sp, cells) * elAmplitude
	}

	// The glow follows the line blurred, so it never striates.
	halo := line
	blur := halo
	for range 3 {
		for i := range n {
			blur[i] = (halo[(i+n-1)%n] + halo[i] + halo[(i+1)%n]) / 3
		}
		halo = blur
	}

	// The current crawls around, brighter and dimmer, though never dark.
	cc := max(cells/3, 2)
	for i := range n {
		x := float64(i) / float64(n) * float64(cc)
		c := 0.5 + 0.5*noise(x-t*sp*0.08*float64(cc), t*0.35, cc, 44)
		crawl := 0.35 + 0.65*smoothstep(0.2, 0.8, c)
		e.points[i*4], e.points[i*4+1], e.points[i*4+2] = line[i], halo[i], crawl
	}
	e.beads = float32(math.Mod(t*0.07*sp, 1))

	// Now and then, a spark: each of a few slots tries every 0.3 to 0.55s,
	// and one that flies shoots out in 35ms and is gone in 150 to 250.
	clear(e.sparkAt[:])
	clear(e.sparkLife[:])
	if still {
		return
	}
	for j := range elSparks {
		period := 0.3 + 0.25*float64(hash(j, 0, 70))
		tau := t/period + float64(hash(j, 1, 70))
		round := int(math.Floor(tau))
		age := (tau - float64(round)) * period
		if hash(j, round, 71) > elSparkRate*0.5 {
			continue
		}
		life := 0.15 + 0.1*float64(hash(j, round, 72))
		if age > life {
			continue
		}
		length := elSparkLen * (0.6 + 0.6*hash(j, round, 73))
		grow := 1 - math.Pow(1-min(age/0.035, 1), 3)
		lit := float64(smoothstep(0, 0.01, float32(age))) * math.Exp(-age/(life*0.4)) * (1 - float64(smoothstep(float32(0.75*life), float32(life), float32(age))))
		f := j * 4
		e.sparkAt[f], e.sparkAt[f+1], e.sparkAt[f+2] = hash(j, round, 74)*total, length, float32(grow)
		e.sparkLife[f], e.sparkLife[f+1] = float32(lit), float32(1-age/life)
		// It wanders sideways as it goes, kinked as lightning is.
		acc := float32(0)
		for i := 1; i < elWalk; i++ {
			acc = min(max(acc+gauss3(j*7919+round, i, 75)*length*0.1, -length*0.3), length*0.3)
			e.sparkWalk[j*elWalk+i] = acc
		}
	}
}

// drawElectricity draws the charged line around r, rounded by radius, its
// outline inset by elMargin from r, t seconds in (still, a pose that
// holds), at intensity a, on a dark background or a light one.
func drawElectricity(p *ui.Painter, r ui.Rect, radius float32, t float64, still bool, a float32, dark bool) {
	w, h := r.W-2*elMargin, r.H-2*elMargin
	rad := min(radius, w/2, h/2)
	total := 2*(w+h-4*rad) + 2*math.Pi*rad
	var e electricity
	e.frame(total, t, still)
	v := electricValues.
		Float(elInset, elMargin).Float(elRadius, radius).Float(elGap, elOut).
		Float(elWidth, elCore).Float(elGlow, elSheath).
		Int(elCount, int32(e.count)).Floats(elPoints, e.points[:e.count*4]...).
		Float(elBeads, e.beads).Float(elIntensity, a*elBrightness).
		Floats(elSparkAt, e.sparkAt[:]...).Floats(elSparkLife, e.sparkLife[:]...).Floats(elSparkWalk, e.sparkWalk[:]...)
	if dark {
		v.Float(elInk, 0).Color(elTint, boltGlow).Color(elHot, boltCore).Color(elCool, boltCool)
	} else {
		// White light is lost on white: deeper colors, painted over it.
		v.Float(elInk, 1).Color(elTint, boltGlow).Color(elHot, ui.Hex("#1d6fe0")).Color(elCool, boltCool)
	}
	electricShader.Draw(p, r, shader.Options{Values: v})
}
