package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// 自定义主题结构体
type CustomTheme struct {
}

func (c CustomTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	//log.Println("name:" + string(name) + ", variant:" + strconv.FormatUint(uint64(variant), 10) + ", color:" + fmt.Sprintf("%#v", theme.DefaultTheme().Color(name, variant)))
	if name == theme.ColorNameDisabled {
		if variant == theme.VariantLight {
			return color.RGBA{R: 128, G: 128, B: 128, A: 255}
		} else {
			return color.RGBA{R: 128, G: 128, B: 128, A: 255}
		}
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (c CustomTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (c CustomTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (c CustomTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(name)
}
