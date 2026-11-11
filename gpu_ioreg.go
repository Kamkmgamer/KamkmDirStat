package main

import (
	"regexp"
	"strconv"
	"strings"
)

// macOS lists GPU statistics under IOAccelerator in the I/O Registry. This parser has no
// macOS dependencies so it is tested on every platform.

var (
	ioregBlock = regexp.MustCompile(`(?m)^\+-o (\S+)`)
	ioregUse   = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	ioregMem   = regexp.MustCompile(`"In use system memory"=(\d+)`)
	ioregVRAM  = regexp.MustCompile(`"vramUsedBytes"=(\d+)`)
	ioregModel = regexp.MustCompile(`"model" = <?"([^"]+)"`)
	ioregCores = regexp.MustCompile(`"gpu-core-count" = (\d+)`)
)

// parseIoregGPUs turns ioreg's IOAccelerator listing into GPU entries.
func parseIoregGPUs(text, cpuModel string) ([]map[string]any, string) {
	gpus := []map[string]any{}
	starts := ioregBlock.FindAllStringSubmatchIndex(text, -1)
	for i, m := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block, class := text[m[0]:end], text[m[2]:m[3]]
		use := ioregUse.FindStringSubmatch(block)
		if use == nil {
			continue
		}
		vendor, name := "", class
		switch {
		case strings.HasPrefix(class, "AGX"):
			vendor, name = "Apple", strings.TrimSpace(cpuModel+" GPU")
			if c := ioregCores.FindStringSubmatch(block); c != nil {
				name += " (" + c[1] + " cores)"
			}
		case strings.Contains(class, "AMD"):
			vendor = "AMD"
		case strings.Contains(class, "Intel"):
			vendor = "Intel"
		case strings.Contains(class, "NVDA") || strings.Contains(class, "GeForce"):
			vendor = "NVIDIA"
		}
		if model := ioregModel.FindStringSubmatch(block); model != nil {
			name = model[1]
		}
		util, _ := strconv.ParseFloat(use[1], 64)
		gpu := map[string]any{"vendor": vendor, "name": name, "util": util, "memUsed": nil, "memTotal": nil, "temp": nil, "power": nil}
		for _, re := range []*regexp.Regexp{ioregVRAM, ioregMem} {
			if mem := re.FindStringSubmatch(block); mem != nil {
				used, _ := strconv.ParseFloat(mem[1], 64)
				gpu["memUsed"] = used
				break
			}
		}
		gpus = append(gpus, gpu)
	}
	if len(gpus) == 0 {
		return gpus, "No GPU statistics found."
	}
	return gpus, ""
}
