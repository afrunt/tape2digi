package main

import (
	"os"

	"github.com/afrunt/tape2digi/internal/i18n"
	"github.com/afrunt/tape2digi/internal/spectrum"
)

// SideSpectrogram returns a spectrogram of the side's mono mix for
// [fromMs, toMs); toMs 0 means the end of the side.
func (a *App) SideSpectrogram(side string, fromMs, toMs int64, columns, rows int) (spectrum.Picture, error) {
	p, err := a.withProject()
	if err != nil {
		return spectrum.Picture{}, err
	}
	path, err := p.AudioPath(side)
	if err != nil {
		return spectrum.Picture{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return spectrum.Picture{}, i18n.New("project.sideHasNoAudio", side)
	}
	return spectrum.Compute(a.jobContext(), path, fromMs, toMs, columns, rows)
}
