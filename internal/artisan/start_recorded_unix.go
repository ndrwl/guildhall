//go:build unix

package artisan

import (
	"errors"
	"os"
	"os/exec"
)

func StartRecorded(u Unit, cmd *exec.Cmd, record func(Identity) error) (Identity, error) {
	if len(cmd.ExtraFiles) != 0 {
		return Identity{}, errors.New("recorded start requires no extra files")
	}
	read, write, err := os.Pipe()
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	argv := append([]string{"sh", "-c", "IFS= read -r gate <&3 && [ \"$gate\" = run ] || exit 125; exec 3<&-; exec \"$@\"", "sh", cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args = "/bin/sh", argv
	cmd.ExtraFiles = []*os.File{read}
	id, err := u.Start(cmd)
	if err != nil {
		return id, err
	}
	if err := record(id); err != nil {
		return id, errors.Join(err, u.Close())
	}
	if _, err := write.WriteString("run\n"); err != nil {
		return id, errors.Join(err, u.Close())
	}
	return id, nil
}
