# KamkmDirStat product brief

## Platform and stack

Desktop application for Windows, Linux and macOS written in Go. The interface is one HTML page shown with WebView2 on Windows, WebKitGTK on Linux and WebKit on macOS. It inspects this computer or SSH-accessible machines. The Windows executable can be cross-compiled on Linux.

## Purpose

Inspect disk usage on this PC (one, some or all drives, or a folder) and on SSH-accessible machines through a folder list and an interactive treemap inspired by WinDirStat, and monitor CPU, memory, GPU, network and processes live.

## This PC and SSH server

This PC and one SSH server are separate tabs that run side by side. This PC is monitored from start-up, and connecting to a server adds a second monitor and a second scan beside it. Switching tabs changes only what is shown: the machine in the background keeps monitoring, keeps recording its history and keeps its finished scan and the folder it was browsing. Searches, the folder list, the map and opening files follow the selected tab.

## Deleting and cleaning up

Items of a finished scan of this PC can be moved to the Recycle Bin (Trash) or deleted permanently after confirmation. Clean up matches the finished scan, while it is still in memory, against rules for dependency folders, build output, package, build and browser caches, downloaded models, temporary files and old installers, and asks git, Docker and the Recycle Bin about agent worktrees, Docker data and deleted files when it opens. Nothing is removed outside the scanned folders, through links, or without confirmation. Deleted items are recorded beside the scan's cache file, which is never rewritten, so views, the map and search leave them out at once.

## Connections

Support SSH config aliases, agent authentication, passwords, and private keys. Verify the server host key before sending credentials: keys in ~/.ssh/known_hosts or ones the user chose to always trust are accepted without asking, and a changed key is warned about. Credentials are not saved.

## Remote reports

Complete scans and size aggregation on the SSH machine before reporting back. On Linux servers a native scanner, stored in ~/.cache/kamkmdirstat when the user allows it, walks folders in parallel; otherwise Python 3 aggregates GNU find output. Detailed results remain in the remote session, and the desktop app requests completed folder reports on demand. Use streamed listings or SFTP as fallbacks when remote aggregation is unavailable.

## Activity monitoring

A Python sampler on the Linux server reads `/proc` and GPU tools over a separate SSH session and reports once a second. Any account can end its own processes. Root or sudo accounts can also end other users' processes and free the page cache; sudo passwords are requested per action and never stored.

## This PC

Local scans of whole NTFS drives read the master file table directly when the app runs as administrator; otherwise they list folders with FindFirstFileEx. Neither follows junctions, symbolic links or mounted volumes. The local monitor uses the same system queries as Task Manager, DXGI and the GPU performance counters. Running as administrator stands in for root: it allows ending other users' processes and clearing the standby cache, and the app offers to restart itself elevated.

On Linux the local monitor runs the same Python sampler as on servers. On macOS it uses Mach and libproc statistics, with GPU use from the I/O Registry; administrators are asked for their password to end other users' processes or run purge.
