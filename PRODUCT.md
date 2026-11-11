# KamkmDirStat product brief

## Platform and stack

Windows desktop application written in Go with a WebView2 interface. It inspects this PC or SSH-accessible machines. The Windows executable can be cross-compiled on Linux.

## Purpose

Inspect disk usage on this PC (one, some or all drives, or a folder) and on SSH-accessible machines through a folder list and an interactive treemap inspired by WinDirStat, and monitor CPU, memory, GPU, network and processes live.

## Connections

Support SSH config aliases, agent authentication, passwords, and private keys. Verify the server host-key fingerprint before sending credentials. Credentials are not saved.

## Remote reports

Complete scans and size aggregation on the SSH machine before reporting back. Detailed results remain in the remote session, and the desktop app requests completed folder reports on demand. Use streamed listings or SFTP as fallbacks when remote aggregation is unavailable.

## Activity monitoring

A Python sampler on the Linux server reads `/proc` and GPU tools over a separate SSH session and reports once a second. Any account can end its own processes. Root or sudo accounts can also end other users' processes and free the page cache; sudo passwords are requested per action and never stored.

## This PC

Local scans list folders directly with FindFirstFileEx and do not follow junctions, symbolic links or mounted volumes. The local monitor uses the same system queries as Task Manager, DXGI and the GPU performance counters. Running as administrator stands in for root: it allows ending other users' processes and clearing the standby cache, and the app offers to restart itself elevated.
