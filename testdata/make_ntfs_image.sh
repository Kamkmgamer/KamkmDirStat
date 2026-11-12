#!/bin/sh
# Builds an NTFS image for TestNTFSImage. Needs root, mkntfs and ntfs-3g (package ntfs-3g).
# Usage: sudo testdata/make_ntfs_image.sh DIR
# Writes DIR/volume.img, DIR/files.txt (inode, size and path of each file) and DIR/dirs.txt,
# as the mounted file system shows them.
set -eu
dir=$1
img=$dir/volume.img
mnt=$dir/mnt
truncate -s 64M "$img"
mkntfs -q -F -f -L kamkm-test "$img"
mkdir -p "$mnt"
ntfs-3g "$img" "$mnt"
trap 'umount "$mnt" 2>/dev/null || true' EXIT
cd "$mnt"

mkdir -p docs/nested/deeper "Ünïcode 文件夹" many "a folder with a long name" empty
printf 'small' > docs/small.txt                       # small enough to be stored in its record
head -c 300000 /dev/urandom > docs/big.bin            # stored in clusters elsewhere
head -c 70000 /dev/zero > docs/nested/zeros.bin
: > docs/nested/deeper/empty.txt
head -c 1234 /dev/zero > "Ünïcode 文件夹/файл с длинным именем.txt"
head -c 4321 /dev/zero > "a folder with a long name/Another Long File Name.document"
# Enough files that the table spans several reads.
i=1
while [ "$i" -le 5000 ]; do
	head -c $((i % 3000)) /dev/zero > "many/file-$i.dat"
	i=$((i + 1))
done
# Appending to two files in turn fragments them into many runs.
i=0
while [ "$i" -lt 100 ]; do
	head -c 8192 /dev/zero >> docs/fragmented-a.bin
	head -c 8192 /dev/zero >> docs/fragmented-b.bin
	i=$((i + 1))
done
# A second name for a file: it must be counted once.
ln docs/big.bin "hard link.bin"
# Links are not followed or listed. ln -s would make Interix links, which Windows treats as
# ordinary files, so write real Windows reparse points: a junction and a symbolic link.
mkdir junction
: > symlink
python3 - <<'PY'
import os, struct

def reparse(tag, substitute, printed, extra=b''):
    s, p = (substitute + '\0').encode('utf-16-le'), (printed + '\0').encode('utf-16-le')
    names = struct.pack('<HHHH', 0, len(s) - 2, len(s), len(p) - 2)
    data = names + extra + s + p
    return struct.pack('<IHH', tag, len(data), 0) + data

os.setxattr('junction', 'system.ntfs_reparse_data', reparse(0xA0000003, '\\??\\C:\\docs', 'C:\\docs'))
os.setxattr('symlink', 'system.ntfs_reparse_data', reparse(0xA000000C, 'docs\\small.txt', 'docs\\small.txt', struct.pack('<I', 1)))
PY
# Deleted files leave unused records behind.
i=0
while [ "$i" -lt 200 ]; do
	head -c 100 /dev/zero > "deleted-$i.tmp"
	i=$((i + 1))
done
rm -f deleted-*.tmp

find . -type f -printf '%i\t%s\t%P\n' > "$dir/files.txt"
find . -mindepth 1 -type d -printf '%P\n' > "$dir/dirs.txt"
cd /
sync
umount "$mnt"
trap - EXIT
rmdir "$mnt"
