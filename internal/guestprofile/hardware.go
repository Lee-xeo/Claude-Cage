package guestprofile

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
)

// Public product names, not device serial numbers or claims of USB passthrough.
//
//go:embed audio-models.json
var audioModelsJSON []byte

type AudioModel struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	Label  string `json:"label"`
	Stem   string `json:"stem"`
	Source string `json:"source"`
}

func AudioModels() []AudioModel {
	var models []AudioModel
	if json.Unmarshal(audioModelsJSON, &models) != nil {
		return nil
	}
	return models
}

func findAudioModel(id string) (AudioModel, bool) {
	for _, m := range AudioModels() {
		if m.ID == id {
			return m, true
		}
	}
	return AudioModel{}, false
}

func chooseAudio(o *Options) error {
	models := AudioModels()
	if len(models) == 0 {
		return errors.New("音频型号库不可用")
	}
	index, err := choose(len(models))
	if err != nil {
		return err
	}
	model := models[index]
	if o.AudioModel != "" {
		var ok bool
		model, ok = findAudioModel(o.AudioModel)
		if !ok {
			return errors.New("音频型号不存在")
		}
	}
	auto := o.AudioName == "" && o.AudioDescription == ""
	if o.AudioName == "" {
		id, err := randomText(8)
		if err != nil {
			return err
		}
		o.AudioName = "alsa_output.usb-" + model.Stem + "_" + strings.ToUpper(id) + "-00.analog-stereo"
	}
	if o.AudioDescription == "" {
		o.AudioDescription = model.Label
	}
	if auto {
		o.AudioModel = model.ID
	}
	return nil
}

func naturalUsername() (string, error) {
	names := []string{"alex", "sam", "jordan", "taylor", "casey", "jamie", "morgan", "robin", "riley", "lee", "kai", "chris", "charlie", "drew", "micah", "sage"}
	i, err := choose(len(names))
	if err != nil {
		return "", err
	}
	n, err := choose(100)
	if err != nil {
		return "", err
	}
	// Two decimal digits are a common account-name suffix. They are not an ID.
	return names[i] + string(rune('0'+n/10)) + string(rune('0'+n%10)), nil
}
