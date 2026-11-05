package mygoscript

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// setter applies the element prop name, which it takes from p.
type setter func(e ui.Element, p *props, name string) ui.Element

func f32Prop(set func(ui.Element, float32) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element { return set(e, p.f32(name, 0)) }
}

func intProp(set func(ui.Element, int) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element { return set(e, p.int(name, 0)) }
}

func strProp(set func(ui.Element, string) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element { return set(e, p.str(name, "")) }
}

func boolProp(set func(ui.Element, bool) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element { return set(e, p.bool(name, false)) }
}

// flag applies a method that takes no argument when the prop is true.
func flag(set func(ui.Element) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element {
		if p.bool(name, false) {
			return set(e)
		}
		return e
	}
}

func colorProp(set func(ui.Element, ui.Color) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element {
		if c, ok := p.color(name); ok {
			return set(e, c)
		}
		return e
	}
}

func floatsProp(set func(ui.Element, ...float32) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element {
		if v := p.floats(name); v != nil {
			return set(e, v...)
		}
		return e
	}
}

func alignProp(set func(ui.Element, ui.Align) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element {
		if a, ok := p.align(name); ok {
			return set(e, a)
		}
		return e
	}
}

// object decodes a prop holding a table of named fields.
func object[T any](p *props, name string) (T, bool) {
	raw, ok := p.take(name)
	var v T
	if !ok {
		return v, false
	}
	// Luau has one empty table, which the prelude encodes as [].
	if string(raw) == "[]" {
		return v, true
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		p.fail(name, fmt.Errorf("bad table: %s", raw))
		return v, false
	}
	return v, true
}

type shadow struct {
	X, Y, Blur, Spread float32
	Color              string
}

func shadowProp(set func(e ui.Element, x, y, blur, spread float32, c ui.Color) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element {
		s, ok := object[shadow](p, name)
		if !ok {
			return e
		}
		c, err := parseColor(s.Color)
		if err != nil {
			p.fail(name, err)
			return e
		}
		return set(e, s.X, s.Y, s.Blur, s.Spread, c)
	}
}

func tracksProp(set func(ui.Element, ...ui.Track) ui.Element) setter {
	return func(e ui.Element, p *props, name string) ui.Element {
		var tracks []ui.Track
		for _, s := range p.strs(name) {
			switch {
			case s == "fit":
				tracks = append(tracks, ui.FitContent())
			case strings.HasSuffix(s, "fr"):
				f, err := strconv.ParseFloat(strings.TrimSuffix(s, "fr"), 32)
				if err != nil {
					p.fail(name, fmt.Errorf("%q is not a track", s))
					return e
				}
				tracks = append(tracks, ui.Fr(float32(f)))
			default:
				f, err := strconv.ParseFloat(s, 32)
				if err != nil {
					p.fail(name, fmt.Errorf("%q is not a track: use \"1fr\", \"120\" or \"fit\"", s))
					return e
				}
				tracks = append(tracks, ui.Fixed(float32(f)))
			}
		}
		if len(tracks) == 0 {
			return e
		}
		return set(e, tracks...)
	}
}

// pair decodes a prop of two numbers, or of one used twice when min is 1.
func pair(p *props, name string, min int) []float32 {
	v := p.floats(name)
	switch {
	case v == nil:
		return nil
	case len(v) == 1 && min == 1:
		return []float32{v[0], v[0]}
	case len(v) != 2:
		p.fail(name, fmt.Errorf("want { a, b }, got %d numbers", len(v)))
		return nil
	}
	return v
}

var fits = map[string]ui.Fit{"contain": ui.Contain, "cover": ui.Cover, "fill": ui.FillBox, "scaleDown": ui.ScaleDown, "natural": ui.NaturalSize}

// elementProps are the props every widget takes: MyGo's Element methods
// for layout, text, color, borders, effects and accessibility. Methods
// that take Go callbacks (Draw, HandleInput, the animations) are not here.
var elementProps = map[string]setter{
	"width": f32Prop(ui.Element.Width), "height": f32Prop(ui.Element.Height),
	"minWidth": f32Prop(ui.Element.MinWidth), "minHeight": f32Prop(ui.Element.MinHeight),
	"maxWidth": f32Prop(ui.Element.MaxWidth), "maxHeight": f32Prop(ui.Element.MaxHeight),
	"widthPercent": f32Prop(ui.Element.WidthPercent), "heightPercent": f32Prop(ui.Element.HeightPercent),
	"minWidthPercent": f32Prop(ui.Element.MinWidthPercent), "minHeightPercent": f32Prop(ui.Element.MinHeightPercent),
	"maxWidthPercent": f32Prop(ui.Element.MaxWidthPercent), "maxHeightPercent": f32Prop(ui.Element.MaxHeightPercent),
	"grow": f32Prop(ui.Element.Grow), "shrink": f32Prop(ui.Element.Shrink),
	"basis": f32Prop(ui.Element.Basis), "basisPercent": f32Prop(ui.Element.BasisPercent),
	"aspectRatio": f32Prop(ui.Element.AspectRatio),
	"gap":         f32Prop(ui.Element.Gap), "gapX": f32Prop(ui.Element.GapX), "gapY": f32Prop(ui.Element.GapY),
	"padding": floatsProp(ui.Element.Padding), "margin": floatsProp(ui.Element.Margin),
	"paddingX": f32Prop(ui.Element.PaddingX), "paddingY": f32Prop(ui.Element.PaddingY),
	"paddingStart": f32Prop(ui.Element.PaddingStart), "paddingEnd": f32Prop(ui.Element.PaddingEnd),
	"marginX": f32Prop(ui.Element.MarginX), "marginY": f32Prop(ui.Element.MarginY),
	"marginStart": f32Prop(ui.Element.MarginStart), "marginEnd": f32Prop(ui.Element.MarginEnd),
	"left": f32Prop(ui.Element.Left), "top": f32Prop(ui.Element.Top),
	"right": f32Prop(ui.Element.Right), "bottom": f32Prop(ui.Element.Bottom),
	"start": f32Prop(ui.Element.Start), "end": f32Prop(ui.Element.End),
	"leftPercent": f32Prop(ui.Element.LeftPercent), "topPercent": f32Prop(ui.Element.TopPercent),
	"rightPercent": f32Prop(ui.Element.RightPercent), "bottomPercent": f32Prop(ui.Element.BottomPercent),
	"fill": flag(ui.Element.Fill), "fillWidth": flag(ui.Element.FillWidth), "fillHeight": flag(ui.Element.FillHeight),
	"center": flag(ui.Element.Center), "absolute": flag(ui.Element.Absolute),
	"alignItems": alignProp(ui.Element.AlignItems), "alignSelf": alignProp(ui.Element.AlignSelf),
	"alignContent": alignProp(ui.Element.AlignContent), "justify": alignProp(ui.Element.Justify),
	"justifyItems": alignProp(ui.Element.JustifyItems), "justifySelf": alignProp(ui.Element.JustifySelf),
	"textAlign": alignProp(ui.Element.TextAlign),
	"wrap":      flag(ui.Element.Wrap), "wrapReverse": flag(ui.Element.WrapReverse), "reverse": flag(ui.Element.Reverse),
	"columns": intProp(ui.Element.Columns), "gridRows": intProp(ui.Element.GridRows),
	"columnSpan": intProp(ui.Element.ColumnSpan), "rowSpan": intProp(ui.Element.RowSpan),
	"columnStart": intProp(ui.Element.ColumnStart), "rowStart": intProp(ui.Element.RowStart),
	"columnTracks": tracksProp(ui.Element.ColumnTracks), "rowTracks": tracksProp(ui.Element.RowTracks),
	"clip": flag(ui.Element.Clip), "clipX": flag(ui.Element.ClipX), "clipY": flag(ui.Element.ClipY),

	"fontSize": f32Prop(ui.Element.FontSize), "fontWeight": intProp(ui.Element.FontWeight),
	"font": strProp(ui.Element.Font), "lang": strProp(ui.Element.Lang),
	"bold": flag(ui.Element.Bold), "italic": flag(ui.Element.Italic),
	"underline": flag(ui.Element.Underline), "wavyUnderline": flag(ui.Element.WavyUnderline),
	"strikethrough": flag(ui.Element.Strikethrough),
	"letterSpacing": f32Prop(ui.Element.LetterSpacing), "lineHeight": f32Prop(ui.Element.LineHeight),
	"fixedLineHeight": f32Prop(ui.Element.FixedLineHeight),
	"singleLine":      flag(ui.Element.SingleLine), "noWrap": flag(ui.Element.NoWrap),
	"maxLines": intProp(ui.Element.MaxLines), "ellipsis": strProp(ui.Element.Ellipsis),
	"selectable": flag(ui.Element.Selectable), "unselectable": flag(ui.Element.Unselectable),
	"decorationThickness": f32Prop(ui.Element.DecorationThickness),

	"background": colorProp(ui.Element.Background), "textColor": colorProp(ui.Element.TextColor),
	"borderColor": colorProp(ui.Element.BorderColor), "decorationColor": colorProp(ui.Element.DecorationColor),
	"selectionColor": colorProp(ui.Element.SelectionColor), "textBackground": colorProp(ui.Element.TextBackground),
	"borderWidth": floatsProp(ui.Element.BorderWidth), "radius": floatsProp(ui.Element.Radius),
	"opacity": f32Prop(ui.Element.Opacity), "blur": f32Prop(ui.Element.Blur),
	"grayscale": flag(ui.Element.Grayscale), "invisible": flag(ui.Element.Invisible),
	"rotate": f32Prop(ui.Element.Rotate),
	"shadow": shadowProp(ui.Element.Shadow), "insetShadow": shadowProp(ui.Element.InsetShadow),
	"border": func(e ui.Element, p *props, name string) ui.Element {
		b, ok := object[struct {
			Width float32
			Color string
		}](p, name)
		if !ok {
			return e
		}
		c, err := parseColor(b.Color)
		if err != nil {
			p.fail(name, err)
			return e
		}
		return e.Border(b.Width, c)
	},
	"dividers": func(e ui.Element, p *props, name string) ui.Element {
		b, ok := object[struct {
			Width float32
			Color string
		}](p, name)
		if !ok {
			return e
		}
		c, err := parseColor(b.Color)
		if err != nil {
			p.fail(name, err)
			return e
		}
		return e.Dividers(b.Width, c)
	},
	"gradient": func(e ui.Element, p *props, name string) ui.Element {
		g, ok := object[struct {
			From, To string
			Angle    float32
		}](p, name)
		if !ok {
			return e
		}
		from, err1 := parseColor(g.From)
		to, err2 := parseColor(g.To)
		if err1 != nil || err2 != nil {
			p.fail(name, fmt.Errorf("from and to must be colors"))
			return e
		}
		return e.Gradient(from, to, g.Angle)
	},
	"scale": func(e ui.Element, p *props, name string) ui.Element {
		v := pair(p, name, 1)
		if v == nil {
			return e
		}
		return e.Scale(v[0], v[1])
	},
	"translate": func(e ui.Element, p *props, name string) ui.Element {
		if v := pair(p, name, 2); v != nil {
			return e.Translate(v[0], v[1])
		}
		return e
	},
	"size": func(e ui.Element, p *props, name string) ui.Element {
		if v := pair(p, name, 2); v != nil {
			return e.Size(v[0], v[1])
		}
		return e
	},
	"lines": func(e ui.Element, p *props, name string) ui.Element {
		if v := pair(p, name, 2); v != nil {
			return e.Lines(int(v[0]), int(v[1]))
		}
		return e
	},
	"fit": func(e ui.Element, p *props, name string) ui.Element {
		s := p.str(name, "")
		f, ok := fits[s]
		if !ok {
			p.fail(name, fmt.Errorf("%q is not one of contain, cover, fill, scaleDown, natural", s))
			return e
		}
		return e.Fit(f)
	},

	"label": strProp(ui.Element.Label), "tooltip": strProp(ui.Element.Tooltip),
	"placeholder": strProp(ui.Element.Placeholder), "description": strProp(ui.Element.Description),
	"error":    strProp(ui.Element.Error),
	"disabled": boolProp(ui.Element.Disabled), "readOnly": boolProp(ui.Element.ReadOnly),
	"checked": boolProp(ui.Element.Checked), "selected": boolProp(ui.Element.Selected),
	"expanded": boolProp(ui.Element.Expanded), "focusRing": boolProp(ui.Element.FocusRing),
	"password": flag(ui.Element.Password), "autoFocus": flag(ui.Element.AutoFocus),
	"focusable": flag(ui.Element.Focusable), "passThrough": flag(ui.Element.PassThrough),
	"mixed": flag(ui.Element.Mixed), "vertical": flag(ui.Element.Vertical),
	"step":  func(e ui.Element, p *props, name string) ui.Element { return e.Step(p.num(name, 0)) },
	"level": intProp(ui.Element.Level),
}

// stateStyles apply their table of element props while the element is
// in that state, as of the frame being built.
var stateStyles = map[string]func(ui.Element) bool{
	"hovered": ui.Element.Hovered, "pressed": ui.Element.Pressed,
	"focused": ui.Element.Focused, "highlighted": ui.Element.Highlighted,
}

// applyProps applies the element props left in p, then reports what is
// still left over.
func applyProps(e ui.Element, p *props) (ui.Element, error) {
	names := make([]string, 0, len(p.m))
	for k := range p.m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		if set, ok := elementProps[name]; ok {
			e = set(e, p, name)
			continue
		}
		on, ok := stateStyles[name]
		if !ok {
			continue
		}
		nested, ok := object[map[string]json.RawMessage](p, name)
		if !ok || !on(e) {
			continue
		}
		sub := &props{kind: p.kind + "." + name, m: nested}
		for k := range nested {
			if set, ok := elementProps[k]; ok {
				e = set(e, sub, k)
			}
		}
		if err := sub.leftover(); err != nil && p.err == nil {
			p.err = err
		}
	}
	return e, p.leftover()
}
