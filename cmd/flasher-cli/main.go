// Command flasher-cli writes disk images to USB drives from the terminal.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"flasher/internal/device"
	"flasher/internal/drives"
	"flasher/internal/flash"
	"flasher/internal/plan"
	"flasher/internal/source"
	"flasher/internal/units"
)

const usage = `usage:
  flasher-cli info IMAGE
  flasher-cli list [-all] [-include-virtual]
  flasher-cli flash [-no-verify] [-yes] [-include-virtual] [-copy [-persistence SIZE]] IMAGE DRIVE
  flasher-cli flash -to-file [-size BYTES] [-no-verify] [-copy [-persistence SIZE]] IMAGE FILE

DRIVE is a path shown by "list", such as /dev/sdb or \\.\PhysicalDrive2
("sdb", "PhysicalDrive2" and "disk2" work too).
With -to-file the image is written into a regular file instead, which is
created at -size bytes if it does not exist.
With -copy a Linux ISO's files are copied to a FAT32 partition that boots
on UEFI, like Rufus's ISO mode, instead of the ISO being written as it is
(dd mode). -persistence adds a
partition of SIZE (such as 8GB) where an Ubuntu or Debian live system
keeps its changes. "info" says whether an ISO supports these.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "info":
		err = info(os.Args[2:])
	case "list":
		err = list(os.Args[2:])
	case "flash":
		err = flashCmd(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func info(args []string) error {
	if len(args) != 1 {
		return errors.New("info takes one image path")
	}
	img, err := source.Inspect(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s\n  type: %s\n", img.Path, img.Description())
	if img.Label != "" {
		fmt.Printf("  label: %s\n", img.Label)
	}
	fmt.Printf("  file size: %s\n", units.Bytes(img.FileSize))
	if img.Size >= 0 {
		fmt.Printf("  image size: %s\n", units.Bytes(img.Size))
	} else {
		fmt.Println("  image size: unknown until decompressed")
	}
	if l := img.Linux; l != nil {
		fmt.Printf("  -copy: %d files, %s\n", l.Files, units.Bytes(l.Bytes))
		switch l.Persistence {
		case source.Casper:
			fmt.Println("  -persistence: yes (casper)")
		case source.LiveBoot:
			fmt.Println("  -persistence: yes (live-boot)")
		}
	}
	return nil
}

func list(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	all := fs.Bool("all", false, "include internal and system drives")
	var virtual bool
	fs.BoolVar(&virtual, "include-virtual", false, "include loop devices and VHDs (for testing)")
	fs.BoolVar(&virtual, "include-loop", false, "same as -include-virtual")
	fs.Parse(args)
	ds, err := drives.List(drives.Options{All: *all, IncludeVirtual: virtual})
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("no removable drives found")
		return nil
	}
	for _, d := range ds {
		fmt.Printf("%-22s %9s  %s\n", d.Path, units.Bytes(d.Size), d.Describe())
	}
	return nil
}

func flashCmd(args []string) error {
	fs := flag.NewFlagSet("flash", flag.ExitOnError)
	noVerify := fs.Bool("no-verify", false, "skip reading the drive back")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	toFile := fs.Bool("to-file", false, "write into a regular file instead of a drive")
	size := fs.Int64("size", 0, "size of a new -to-file target in bytes")
	copyFiles := fs.Bool("copy", false, "copy a Linux ISO's files to FAT32 rather than writing it as it is")
	persistence := fs.String("persistence", "", "size of a partition for a live system's changes, with -copy")
	var virtual bool
	fs.BoolVar(&virtual, "include-virtual", false, "allow loop devices and VHDs as targets (for testing)")
	fs.BoolVar(&virtual, "include-loop", false, "same as -include-virtual")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return errors.New("flash takes an image and a target")
	}
	img, err := source.Inspect(fs.Arg(0))
	if err != nil {
		return err
	}
	target := fs.Arg(1)
	opts, err := planOptions(img, *copyFiles, *persistence)
	if err != nil {
		return err
	}

	var dev device.Device
	var p *plan.Plan
	if *toFile {
		if dev, err = openFileTarget(target, *size, img); err != nil {
			return err
		}
		defer dev.Close()
		if p, err = plan.For(img, dev.Size(), dev.SectorSize(), opts); err != nil {
			return err
		}
	} else {
		d, err := drives.Find(target, drives.Options{All: true, IncludeVirtual: virtual})
		if err != nil {
			return err
		}
		if d.IsSystem {
			return fmt.Errorf("%s holds the running system and will not be overwritten", d.Path)
		}
		fmt.Printf("Writing %s (%s)\n     to %s, %s %s\n", img.Path, img.Description(), d.Ref(), units.Bytes(d.Size), d.Describe())
		if opts.CopyFiles {
			fmt.Println("Copying the ISO's files to a FAT32 partition, which boots on UEFI only.")
		}
		if opts.Persistence > 0 {
			fmt.Printf("Adding a %s partition for the live system's changes.\n", units.Bytes(opts.Persistence))
		}
		if !*yes && !confirm(fmt.Sprintf("Everything on %s (%s) will be erased. Continue?", d.Name(), d.Ref())) {
			return errors.New("cancelled")
		}
		// Plan before opening: opening a drive on Windows already clears
		// its partition table, and a plan can still fail.
		if p, err = plan.For(img, d.Size, d.SectorSize, opts); err != nil {
			return err
		}
		if dev, err = device.Open(d); err != nil {
			p.Close()
			return err
		}
		defer dev.Close()
	}
	defer p.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	start := time.Now()
	err = flash.Run(ctx, dev, p, flash.Options{Verify: !*noVerify, Progress: printProgress})
	fmt.Println()
	if err != nil {
		summary, detail := flash.Explain(err)
		if detail != "" {
			summary += "\n  (" + detail + ")"
		}
		return errors.New(summary)
	}
	fmt.Printf("Done in %s.\n", time.Since(start).Round(time.Second))
	return nil
}

func planOptions(img *source.Image, copyFiles bool, persistence string) (plan.Options, error) {
	opts := plan.Options{CopyFiles: copyFiles}
	if copyFiles && img.Linux == nil {
		return opts, errors.New("-copy works with Linux ISOs whose files all fit on FAT32 and that boot on UEFI, and this image is not one")
	}
	if persistence == "" {
		return opts, nil
	}
	if !copyFiles {
		return opts, errors.New("-persistence needs -copy")
	}
	if img.Linux.Persistence == source.NoPersistence {
		return opts, errors.New("this ISO does not support persistence; it works with Ubuntu (casper) and Debian (live-boot) live ISOs")
	}
	n, err := units.ParseBytes(persistence)
	if err != nil {
		return opts, err
	}
	if n < plan.MinPersistence {
		return opts, fmt.Errorf("-persistence must be at least %s", units.Bytes(plan.MinPersistence))
	}
	opts.Persistence = n
	return opts, nil
}

func openFileTarget(path string, size int64, img *source.Image) (device.Device, error) {
	if _, err := os.Stat(path); err == nil && size == 0 {
		return device.OpenFile(path)
	}
	if size == 0 {
		size = img.Size
	}
	if size <= 0 {
		return nil, errors.New("the image size is unknown, so give the new file a size with -size")
	}
	return device.CreateFile(path, size)
}

func confirm(q string) bool {
	fmt.Printf("%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

func printProgress(p flash.Progress) {
	line := fmt.Sprintf("%-9s %5.1f%%  %s", p.Stage, p.Fraction*100, units.Bytes(p.Bytes))
	if p.BytesPerSec > 0 {
		line += fmt.Sprintf("  %s/s", units.Bytes(int64(p.BytesPerSec)))
	}
	if p.ETA > 0 {
		line += "  " + units.Duration(p.ETA) + " left"
	}
	fmt.Printf("\r%-60s", line)
}
