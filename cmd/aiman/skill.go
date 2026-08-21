package main

import (
	"os"

	"github.com/bouwerp/aiman/internal/aimanskill"
)

func runSkill() error {
	_, err := os.Stdout.WriteString(aimanskill.Text)
	if aimanskill.Text != "" && aimanskill.Text[len(aimanskill.Text)-1] != '\n' {
		_, _ = os.Stdout.WriteString("\n")
	}
	return err
}
