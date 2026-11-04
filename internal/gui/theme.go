package gui

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Custom colour names used by the app's own widgets.
const (
	colorCard       fyne.ThemeColorName = "rarCard"
	colorCardBorder fyne.ThemeColorName = "rarCardBorder"
	colorUp         fyne.ThemeColorName = "rarUp"
	colorDown       fyne.ThemeColorName = "rarDown"
	colorMuted      fyne.ThemeColorName = "rarMuted"
)

func hex(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

func withAlpha(c color.Color, a uint8) color.NRGBA {
	r, g, b, _ := c.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: a}
}

// warmPalette maps colour names to a light (cream, terracotta) and a dark
// (espresso, amber) variant.
var warmPalette = map[fyne.ThemeColorName][2]color.Color{
	theme.ColorNameBackground:          {hex(0xFAF4EC), hex(0x1F1813)},
	colorCard:                          {hex(0xFFFDF9), hex(0x2A211B)},
	colorCardBorder:                    {hex(0xEEDFCD), hex(0x3A2E25)},
	theme.ColorNameOverlayBackground:   {hex(0xFFFDF9), hex(0x2A211B)},
	theme.ColorNameMenuBackground:      {hex(0xFFFDF9), hex(0x2A211B)},
	theme.ColorNameHeaderBackground:    {hex(0xF4E7D7), hex(0x33281F)},
	theme.ColorNameForeground:          {hex(0x3A2A1F), hex(0xF4E8DA)},
	colorMuted:                         {hex(0x9C8573), hex(0xA8927F)},
	theme.ColorNamePlaceHolder:         {hex(0xAD9886), hex(0x8E7A69)},
	theme.ColorNamePrimary:             {hex(0xE0763A), hex(0xF08A4B)},
	theme.ColorNameForegroundOnPrimary: {hex(0xFFFFFF), hex(0x1F1813)},
	theme.ColorNameHyperlink:           {hex(0xC9612A), hex(0xF5A06A)},
	theme.ColorNameButton:              {hex(0xF4E7D7), hex(0x3A2D24)},
	theme.ColorNameDisabledButton:      {hex(0xEFE6DB), hex(0x2F251E)},
	theme.ColorNameDisabled:            {hex(0xC2B09F), hex(0x6B5A4C)},
	theme.ColorNameHover:               {withAlpha(hex(0xE0763A), 0x1c), withAlpha(hex(0xF08A4B), 0x26)},
	theme.ColorNamePressed:             {withAlpha(hex(0xE0763A), 0x33), withAlpha(hex(0xF08A4B), 0x40)},
	theme.ColorNameFocus:               {withAlpha(hex(0xE0763A), 0x55), withAlpha(hex(0xF08A4B), 0x66)},
	theme.ColorNameSelection:           {withAlpha(hex(0xE0763A), 0x33), withAlpha(hex(0xF08A4B), 0x44)},
	theme.ColorNameInputBackground:     {hex(0xF7EEE3), hex(0x332820)},
	theme.ColorNameInputBorder:         {hex(0xE6D3BE), hex(0x4A3A2E)},
	theme.ColorNameSeparator:           {hex(0xEEDFCD), hex(0x3A2E25)},
	theme.ColorNameScrollBar:           {withAlpha(hex(0x3A2A1F), 0x40), withAlpha(hex(0xF4E8DA), 0x40)},
	theme.ColorNameShadow:              {withAlpha(hex(0x7A4A2A), 0x22), withAlpha(hex(0x000000), 0x66)},
	colorUp:                            {hex(0x4F9A6A), hex(0x6DBB86)},
	colorDown:                          {hex(0xD5584A), hex(0xEE7A6B)},
	theme.ColorNameSuccess:             {hex(0x4F9A6A), hex(0x6DBB86)},
	theme.ColorNameError:               {hex(0xD5584A), hex(0xEE7A6B)},
	theme.ColorNameWarning:             {hex(0xE5A23B), hex(0xF0B65A)},
}

// WarmTheme is the app theme: warm, soft and rounded.
type WarmTheme struct{}

var _ fyne.Theme = WarmTheme{}

func (WarmTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if c, ok := warmPalette[n]; ok {
		if v == theme.VariantDark {
			return c[1]
		}
		return c[0]
	}
	return theme.DefaultTheme().Color(n, v)
}

func (WarmTheme) Font(s fyne.TextStyle) fyne.Resource     { return theme.DefaultTheme().Font(s) }
func (WarmTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }

func (WarmTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameText:
		return 14
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius, theme.SizeNameSelectionRadius:
		return 10
	case theme.SizeNameCardRadius, theme.SizeNameDialogRadius:
		return 16
	case theme.SizeNamePopupRadius, theme.SizeNameMenuRadius:
		return 12
	case theme.SizeNameScrollBarRadius:
		return 4
	}
	return theme.DefaultTheme().Size(n)
}

// themeColor reads a colour of the current theme. The app's own colour
// names fall back to the warm palette if the current theme ignores them.
func themeColor(n fyne.ThemeColorName) color.Color {
	if _, own := warmPalette[n]; own && strings.HasPrefix(string(n), "rar") {
		if c := theme.Current().Color(n, variant()); c != nil {
			if _, _, _, a := c.RGBA(); a > 0 {
				return c
			}
		}
		return WarmTheme{}.Color(n, variant())
	}
	return theme.Color(n)
}

func variant() fyne.ThemeVariant {
	if a := fyne.CurrentApp(); a != nil {
		return a.Settings().ThemeVariant()
	}
	return theme.VariantLight
}
