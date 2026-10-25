package app

import _ "embed"

//go:embed icons/lulu.png
var luluDockIcon []byte

//go:embed icons/benben.png
var benbenDockIcon []byte

// SetDockIcon switches the macOS Dock icon to the selected character.
// Platforms without runtime icon support treat it as a no-op.
func (a *App) SetDockIcon(character string) {
	if character == "benben" {
		setDockIcon(benbenDockIcon)
		return
	}
	setDockIcon(luluDockIcon)
}
