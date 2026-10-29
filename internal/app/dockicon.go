package app

import _ "embed"

//go:embed icons/default.png
var defaultDockIcon []byte

//go:embed icons/lulu.png
var luluDockIcon []byte

//go:embed icons/benben.png
var benbenDockIcon []byte

// SetDockIcon switches the macOS Dock icon to the selected character.
// Platforms without runtime icon support treat it as a no-op.
func (a *App) SetDockIcon(character string) {
	switch character {
	case "benben":
		setDockIcon(benbenDockIcon)
	case "lulu":
		setDockIcon(luluDockIcon)
	default:
		setDockIcon(defaultDockIcon)
	}
}
