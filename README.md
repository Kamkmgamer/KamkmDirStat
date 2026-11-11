# KamkmDirStat

A Windows desktop app that shows what is using the disk on an SSH server, in the style of WinDirStat: a size-ordered folder list and a cushion treemap coloured by file type.

## Run

Run `KamkmDirStat.exe` on Windows with the Microsoft Edge WebView2 Runtime and Windows OpenSSH installed. Enter an SSH config alias or hostname and an absolute remote path. Leave the username and port blank to use the values from your SSH config. The default authentication mode uses keys loaded in the SSH agent and unencrypted `IdentityFile` keys from your config. You can also choose password or a specific private key with its passphrase.

Select **Connect and scan**, then compare the resolved server and displayed SHA-256 host-key fingerprint with a trusted fingerprint for that server before choosing **Trust for this scan**. Credentials are not saved and an agent never signs an authentication request until after the host key is checked.

The report appears when the remote scan is complete. Click a folder in the list or the map to open it, use the path at the top to go back up, and hover over the map to see what each block is.

## How scanning works

The app runs the scan and calculates folder totals on the remote machine. It uses GNU `find` and Python 3 through the SSH session; nothing is installed and no scan files are written to disk. Progress counts are sent during the scan. When it finishes, the server sends a compressed overview rather than downloading the complete file list. Clicking a folder retrieves that folder's finished report from the same scan, without rescanning it.

A test scan of roughly 560,000 files completed in about 6 seconds and downloaded about 38 KB for the overview. Performance depends on the server and network. The detailed tree remains in memory on the server until the app closes or a new scan replaces it. Keep the SSH connection open to browse folders that have not been loaded yet. Recently loaded views remain cached locally if the connection closes.

Sizes are disk usage, as reported by `du`. Hard-linked files are counted once and symbolic links are not followed or counted.

**Stay on this file system** (on by default) works like `du -x`: other mounts such as `/proc`, `/sys`, `/dev`, `/boot` and network shares are not scanned. Scan a mount point directly to see it. When the scanned folder is the root of a file system, the app shows an estimated percentage from the file system's inode count.

If Python 3 is unavailable, the app falls back to streaming a compressed `find` listing and calculating the tree locally. That fallback shows partial results while it runs. If the account cannot run commands (for example, an SFTP-only account) or the server lacks GNU `find`, it falls back to listing folders over SFTP. That is much slower on distant servers because each folder needs several network round trips. In that mode, sizes are file sizes rather than disk usage, and only `/proc`, `/sys` and `/dev` are skipped when scanning `/`.

Folders the account cannot read are marked incomplete and counted in the status line. Use an account that can read them, such as root, for complete totals. File contents are never read. You can cancel a scan. A remote scan publishes its report only after completion, so cancelling it does not download partial results; streamed and SFTP fallbacks retain their partial results.

OpenSSH resolves the config through `ssh -G`, including `Host`, `Match`, and `Include` rules. The app uses the resolved host, user, port, identity files, `IdentitiesOnly`, and `IdentityAgent`. Other SSH settings are not generally supported; connections requiring `ProxyJump` or `ProxyCommand` are rejected with an error. Encrypted keys must be loaded into the agent or selected manually with a passphrase. On Windows the default agent is the OpenSSH agent service; if it is stopped, start it and add your key with `ssh-add`, or use an unencrypted identity file or manual login. Known-host persistence is not yet supported.

## Build

Install the Go version specified in `go.mod`. On Windows, run:

```powershell
$env:CGO_ENABLED="0"
go build -trimpath -ldflags="-H=windowsgui" -o KamkmDirStat.exe .
```

On Linux, cross-compile with:

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-H=windowsgui' -o KamkmDirStat.exe .
```

## Test

Run `go test ./...` and `node ui_test.cjs`. The tests use local test servers. Tests marked Linux verify the remote Python report against the existing tree builder, on-demand folder views, remote permission errors and cancellation, and run the streamed `find` script with zstd, gzip and no compression, plus the SFTP fallback, permission errors, progress and cancellation. They also cover SFTP latency, channel limits, agent authentication, OpenSSH config resolution and host-key rejection. `TestLiveScan` scans a real server when `SSE_LIVE_HOST` is set; it trusts the host key it is shown, so use it only with a server you already trust.
