# KamkmDirStat product brief

## Platform and stack

Windows desktop application written in Go with a WebView2 interface. The Windows executable can be cross-compiled on Linux.

## Purpose

Inspect disk usage on SSH-accessible machines through a folder list and an interactive treemap inspired by WinDirStat.

## Connections

Support SSH config aliases, agent authentication, passwords, and private keys. Verify the server host-key fingerprint before sending credentials. Credentials are not saved.

## Remote reports

Complete scans and size aggregation on the SSH machine before reporting back. Detailed results remain in the remote session, and the desktop app requests completed folder reports on demand. Use streamed listings or SFTP as fallbacks when remote aggregation is unavailable.
