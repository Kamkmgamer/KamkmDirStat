package main

import "testing"

// Shortened from ioreg -r -d 1 -w 0 -c IOAccelerator on an Apple Silicon Mac and an Intel
// Mac with an AMD GPU.
const ioregSample = `+-o AGXAcceleratorG13G  <class AGXAcceleratorG13G, id 0x1000004c7, registered, matched, active, busy 0 (0 ms), retain 46>
    {
      "IOClass" = "AGXAcceleratorG13G"
      "gpu-core-count" = 8
      "PerformanceStatistics" = {"In use system memory (driver)"=0,"Alloc system memory"=1217314816,"Tiler Utilization %"=6,"recoveryCount"=0,"In use system memory"=411123712,"Renderer Utilization %"=11,"Device Utilization %"=12}
    }

+-o AMDRadeonX5000_AMDNavi10GraphicsAccelerator  <class AMDRadeonX5000_AMDNavi10GraphicsAccelerator, id 0x10000035a, registered, matched, active, busy 0 (0 ms), retain 58>
    {
      "model" = <"AMD Radeon Pro 5500M">
      "PerformanceStatistics" = {"Device Utilization %"=3,"vramUsedBytes"=1073741824,"vramFreeBytes"=3221225472}
    }

+-o IntelAccelerator  <class IntelAccelerator, id 0x100000361, registered, matched, active, busy 0 (0 ms), retain 20>
    {
      "IOClass" = "IntelAccelerator"
    }
`

func TestParseIoregGPUs(t *testing.T) {
	gpus, note := parseIoregGPUs(ioregSample, "Apple M1")
	if note != "" || len(gpus) != 2 {
		t.Fatalf("note %q, %d GPUs: %v", note, len(gpus), gpus)
	}
	apple, amd := gpus[0], gpus[1]
	if apple["name"] != "Apple M1 GPU (8 cores)" || apple["vendor"] != "Apple" || apple["util"] != 12.0 || apple["memUsed"] != 411123712.0 {
		t.Errorf("Apple GPU: %v", apple)
	}
	if amd["name"] != "AMD Radeon Pro 5500M" || amd["vendor"] != "AMD" || amd["util"] != 3.0 || amd["memUsed"] != 1073741824.0 {
		t.Errorf("AMD GPU: %v", amd)
	}
	if _, note := parseIoregGPUs("", "Apple M1"); note == "" {
		t.Error("no note when nothing was found")
	}
}
