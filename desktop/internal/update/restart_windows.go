package update

import (
	"os"
	"os/exec"
)

func restart(path string) error {
	if err := exec.Command(path, os.Args[1:]...).Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
