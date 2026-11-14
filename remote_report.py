import bisect
import collections
import gzip
import heapq
import json
import os
import re
import shutil
import signal
import struct
import subprocess
import sys
import threading
import time

# This process owns the scan and detailed results for one SSH session. Nothing is
# installed or written to disk. Requests after completion retrieve one folder.
#
# Folders are Node objects. Files are (size, name) tuples in their folder's children, which
# is much cheaper with hundreds of thousands of files; the helpers below read either.
class Node:
    __slots__ = ('name', 'parent', 'directory', 'size', 'files', 'children', 'incomplete', 'id')
    def __init__(self, name, parent=None):
        self.name, self.parent, self.directory, self.size = name, parent, True, 0
        self.files, self.children, self.incomplete, self.id = 0, [], False, 0


def sort_key(child):
    if child.__class__ is Node:
        return (-child.size, child.name)
    return (-child[0], child[1])


def entry_size(child):
    return child.size if child.__class__ is Node else child[0]


def send(value):
    payload = gzip.compress(json.dumps(value, ensure_ascii=True, separators=(',', ':')).encode('utf-8'), compresslevel=3)
    sys.stdout.buffer.write(struct.pack('>I', len(payload)))
    sys.stdout.buffer.write(payload)
    sys.stdout.buffer.flush()


def folder_view(dirs, index):
    if type(index) is not int or index < 0 or index >= len(dirs):
        raise ValueError('That folder is not in the scan results.')
    node = dirs[index]
    trail, p = [], node
    while p is not None:
        trail.append({'id': p.id, 'name': p.name})
        p = p.parent
    entries = []
    for child in node.children[:500]:
        if child.__class__ is Node:
            entries.append({'name': child.name, 'size': child.size, 'dir': True,
                            'id': child.id, 'files': child.files, 'incomplete': child.incomplete})
        else:
            entries.append({'name': child[1], 'size': child[0], 'dir': False, 'id': 0, 'files': 1, 'incomplete': False})
    map_root = {'n': node.name, 's': node.size, 'd': True, 'i': node.id}
    queue = collections.deque([(node, map_root)])
    threshold, budget = max(node.size // 4000, 1), 12000
    while queue:
        current, mapped = queue.popleft()
        children, other = [], 0
        for child in current.children:
            folder = child.__class__ is Node
            size = child.size if folder else child[0]
            if size < threshold or budget <= 0:
                other += size
                continue
            budget -= 1
            if folder:
                item = {'n': child.name, 's': size, 'd': True, 'i': child.id}
                if child.children:
                    queue.append((child, item))
            else:
                item = {'n': child[1], 's': size}
            children.append(item)
        if children:
            mapped['c'] = children
        if other:
            mapped['o'] = other
    return {'id': index, 'trail': trail[::-1], 'size': node.size, 'files': node.files,
            'items': len(node.children), 'incomplete': node.incomplete, 'entries': entries,
            'moreCount': max(0, len(node.children) - 500),
            'moreSize': sum(entry_size(child) for child in node.children[500:]), 'map': map_root}


# Search, for when the native scanner cannot run. It follows internal/search in the app:
# every word of the query must appear with its letters in order, whole words and word
# starts rank first, and with no words the largest files are listed. Names are joined
# into one string so a single regular expression, run in C, finds the candidates.
KINDS = {}
for kind, names in (('images', 'jpg jpeg jpe jfif png gif bmp webp tif tiff heic heif avif svg ico psd jxl raw cr2 cr3 nef arw dng orf rw2 raf'),
                    ('videos', 'mp4 m4v mkv mov avi wmv flv webm mpg mpeg m2ts mts 3gp vob ogv divx'),
                    ('audio', 'mp3 wav flac aac m4a ogg oga opus wma aif aiff alac mid midi ape'),
                    ('documents', 'pdf doc docx docm odt rtf txt md xls xlsx xlsm ods csv ppt pptx odp epub mobi azw3 pages numbers key tex'),
                    ('archives', 'zip rar 7z tar gz tgz bz2 xz zst lz4 lzma iso dmg cab')):
    for e in names.split():
        KINDS[e] = kind
SEPARATORS = set(" _-.()[]{}/\\,+&'~@#=")


def file_ext(name):
    i = name.rfind('.')
    return name[i + 1:].lower() if 0 < i and len(name) - i <= 12 else ''


class SearchIndex:
    def __init__(self, dirs):
        self.dirs = dirs
        names, self.parents, self.sizes, self.ids, self.exts = [], [], [], [], []
        types = {}
        for d in dirs:
            for c in d.children:
                folder = c.__class__ is Node
                name = c.name if folder else c[1]
                size = c.size if folder else c[0]
                names.append(name)
                self.parents.append(d.id)
                self.sizes.append(size)
                self.ids.append(c.id if folder else -1)
                if folder:
                    self.exts.append(None)
                else:
                    e = file_ext(name)
                    self.exts.append(e)
                    t = types.get(e)
                    if t is None:
                        t = types[e] = {'ext': e, 'kind': KINDS.get(e, 'other'), 'files': 0, 'size': 0}
                    t['files'] += 1
                    t['size'] += size
        self.names = names
        self.blob = '\0'.join(names)
        self.starts, at = [], 0
        for name in names:
            self.starts.append(at)
            at += len(name) + 1
        self.types = sorted(types.values(), key=lambda t: (-t['size'], t['ext']))
        self.paths = {}

    def path(self, folder):
        p = self.paths.get(folder)
        if p is None:
            names, node = [], self.dirs[folder]
            while node is not None:
                names.append(node.name)
                node = node.parent
            names.reverse()
            p = names[0]
            for n in names[1:]:
                p = p + n if p.endswith('/') else p + '/' + n
            self.paths[folder] = p
        return p

    def search(self, q):
        text = q.get('text') or ''
        if not isinstance(text, str):
            raise ValueError('Invalid search.')
        words = text.lower().split()
        kind = q.get('kind') or ''
        exts = set(str(e).strip().lstrip('.').lower() for e in (q.get('exts') or []) if str(e).strip('. '))
        limit = max(1, min(int(q.get('limit') or 1000), 5000))
        by_size = q.get('sort') == 'size' or not words
        dirs_ok = bool((kind in ('', 'all') and words) or kind == 'folders') and not exts
        files_ok = kind != 'folders'
        want_kind = kind if kind not in ('', 'all', 'files', 'folders') else None

        def accept(i):
            e = self.exts[i]
            if e is None:
                return dirs_ok
            if not files_ok or (want_kind and KINDS.get(e) != want_kind):
                return False
            return not exts or e in exts

        if words:
            patterns = [re.compile('[^\0]*?'.join(re.escape(ch) for ch in w), re.IGNORECASE) for w in words]
            candidates, last = [], -1
            for m in patterns[0].finditer(self.blob):
                i = bisect.bisect_right(self.starts, m.start()) - 1
                if i != last:
                    candidates.append(i)
                    last = i
            for p in patterns[1:]:
                candidates = [i for i in candidates if p.search(self.names[i])]
        else:
            candidates = range(len(self.names))
        hits, total, total_size = [], 0, 0
        for i in candidates:
            if not accept(i):
                continue
            total += 1
            total_size += self.sizes[i]
            if by_size:
                hits.append((self.sizes[i], -i, i))
            else:
                hits.append((self.score(self.names[i], words), -len(self.names[i]), self.sizes[i], -i, i))
        result = []
        for h in heapq.nlargest(limit, hits):
            i = h[-1]
            name = self.names[i]
            hit = {'name': name, 'path': self.path(self.parents[i]), 'size': self.sizes[i], 'parent': self.parents[i]}
            if self.ids[i] >= 0:
                hit['dir'], hit['id'] = True, self.ids[i]
            if words:
                hit['match'] = self.positions(name, words)
            result.append(hit)
        out = {'hits': result, 'total': total, 'totalSize': total_size, 'sort': 'size' if by_size else 'match',
               'files': not words and kind in ('', 'all') and not exts}
        if q.get('types'):
            out['types'] = self.types[:200]
        return out

    @staticmethod
    def find(lower, word):
        # An exact occurrence at a word start, then anywhere; else the letters in order.
        at, first = lower.find(word), -1
        while at >= 0:
            if first < 0:
                first = at
            if at == 0 or lower[at - 1] in SEPARATORS:
                return list(range(at, at + len(word))), True
            at = lower.find(word, at + 1)
        if first >= 0:
            return list(range(first, first + len(word))), True
        pos, j = [], 0
        for k, ch in enumerate(lower):
            if j < len(word) and ch == word[j]:
                pos.append(k)
                j += 1
        return pos, False

    def score(self, name, words):
        lower, total = name.lower(), 0
        for w in words:
            pos, exact = self.find(lower, w)
            if not pos:
                continue
            s = 16 * len(pos) + (24 if exact else 0)
            start = pos[0]
            if start == 0:
                s += 20
            elif lower[start - 1] in SEPARATORS:
                s += 16
            s -= 3 * sum(1 for a, b in zip(pos, pos[1:]) if b - a > 1) + (pos[-1] - pos[0] + 1 - len(pos))
            total += s
        return total

    def positions(self, name, words):
        lower = name.lower()
        if len(lower) != len(name):
            return []
        out = set()
        for w in words:
            out.update(self.find(lower, w)[0])
        return sorted(out)


def run():
    os.environ['LC_ALL'] = 'C'
    root_path, one_fs = sys.argv[1], sys.argv[2] == '1'
    if not shutil.which('find'):
        print('SSE2 unavailable', flush=True)
        return
    if not os.path.isdir(root_path):
        print('SSE2 gzip', flush=True)
        send({'type': 'error', 'error': 'The remote path must be a directory.'})
        return
    probe = subprocess.run(['find', root_path, '-maxdepth', '0', '-printf', ''], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if probe.returncode:
        print('SSE2 unavailable', flush=True)
        return
    print('SSE2 gzip', flush=True)
    root = Node(root_path)
    dirs, stack, rels, by_path, seen = [root], [root], [''], {'': root}, set()
    errors, error_paths = [], []
    error_count, bad = 0, 0
    progress = {'mode': 'remote', 'files': 0, 'directories': 0, 'entries': 0, 'bytes': 0}
    if one_fs and os.path.ismount(root_path):
        stat = os.statvfs(root_path)
        progress['expected'] = max(0, stat.f_files - stat.f_ffree)
    command = ['find', '-H', root_path]
    if one_fs:
        command += ['-xdev']
    command += ['(', '-type', 'f', '-links', '+1', '-printf', r'h %D:%i %b %d %f\0', ')', '-o', '-printf', r'%y %b %d %f\0']
    proc = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    def stop(signum, frame):
        if proc.poll() is None:
            proc.terminate()
        raise SystemExit(0)
    for name in ('SIGHUP', 'SIGTERM', 'SIGINT'):
        if hasattr(signal, name):
            signal.signal(getattr(signal, name), stop)
    def collect_errors():
        nonlocal error_count
        for raw in proc.stderr:
            line = raw.decode('utf-8', 'replace').strip()
            if not line.startswith('find:'):
                continue
            error_count += 1
            if len(errors) < 200:
                errors.append(line)
            if line.startswith("find: '"):
                end = line.rfind("': ")
                if end >= 7 and len(error_paths) < 100000:
                    error_paths.append(line[7:end])
    error_reader = threading.Thread(target=collect_errors, daemon=True)
    error_reader.start()
    last_report = time.monotonic()
    send({'type': 'progress', 'progress': progress})
    try:
        pending = b''
        entries = files = directories = total = 0
        max_blocks = (2**63 - 1) // 512
        decode = bytes.decode
        while True:
            chunk = proc.stdout.read1(256 * 1024)
            if not chunk:
                break
            records = (pending + chunk).split(b'\0')
            pending = records.pop()
            # The hot loop: one find record each, so it avoids function calls and objects
            # for files. Records are "<type> <blocks> <depth> <name>", or for files with
            # several hard links "h <dev:inode> <blocks> <depth> <name>".
            for raw in records:
                try:
                    if raw[:1] == b'h':
                        kind, inode, blocks, depth, name = raw.split(b' ', 4)
                    else:
                        kind, blocks, depth, name = raw.split(b' ', 3)
                        inode = None
                    blocks, depth = int(blocks), int(depth)
                except ValueError:
                    bad += 1
                    continue
                if blocks < 0 or blocks > max_blocks or depth < 0 or depth > 4096 or not name or (depth and b'/' in name):
                    bad += 1
                    continue
                entries += 1
                size = blocks * 512
                if depth == 0:
                    if kind == b'd':
                        root.size += size
                        total += size
                    continue
                if depth > len(stack):
                    bad += 1
                    continue
                if len(stack) > depth:
                    del stack[depth:]
                    del rels[depth:]
                if kind == b'l':
                    continue
                parent = stack[-1]
                name = decode(name, 'utf-8', 'replace')
                if kind == b'd':
                    node = Node(name, parent)
                    node.size = size
                    node.id = len(dirs)
                    dirs.append(node)
                    parent.children.append(node)
                    relative = rels[-1] + '/' + name if rels[-1] else name
                    stack.append(node)
                    rels.append(relative)
                    by_path[relative] = node
                    directories += 1
                else:
                    if inode is not None:
                        if inode in seen:
                            size = 0
                        else:
                            seen.add(inode)
                    parent.children.append((size, name))
                    parent.files += 1
                    parent.size += size
                    files += 1
                total += size
            now = time.monotonic()
            if now - last_report >= 0.75:
                progress.update(entries=entries, files=files, directories=directories, bytes=total)
                send({'type': 'progress', 'progress': progress})
                last_report = now
        progress.update(entries=entries, files=files, directories=directories, bytes=total)
        if pending:
            bad += 1
        code = proc.wait()
        error_reader.join()
        if bad:
            error_count += 1
            errors.append(str(bad) + ' unreadable entries in the server listing')
        if code and not error_count:
            error_count += 1
            errors.append('The server listing ended with exit status ' + str(code))
        for p in error_paths:
            if p == root_path:
                rel = ''
            elif p.startswith(root_path.rstrip('/') + '/'):
                rel = p[len(root_path.rstrip('/')) + 1:]
            else:
                continue
            while rel not in by_path:
                rel = rel.rsplit('/', 1)[0] if '/' in rel else ''
            node = by_path[rel]
            while node is not None and not node.incomplete:
                node.incomplete = True
                node = node.parent
        if error_count:
            root.incomplete = True
        # Children precede parents in this reverse pass, so each subtree is summed once.
        for node in reversed(dirs):
            node.children.sort(key=sort_key)
            if node.parent is not None:
                node.parent.size += node.size
                node.parent.files += node.files
        send({'type': 'done', 'view': folder_view(dirs, 0), 'progress': progress,
              'errors': errors, 'errorCount': error_count, 'features': ['search']})
        index = None
        for line in sys.stdin:
            if len(line) > 4096:
                raise ValueError('Report request is too large.')
            request = json.loads(line)
            if request.get('type') == 'close':
                return
            try:
                if request.get('type') == 'search':
                    if index is None:
                        index = SearchIndex(dirs)
                    send({'type': 'search', 'search': index.search(request.get('query') or {})})
                    continue
                if request.get('type') != 'view':
                    raise ValueError('The server scanner does not understand the request %s.' % request.get('type'))
                send({'type': 'view', 'view': folder_view(dirs, request.get('id'))})
            except (ValueError, TypeError) as err:
                send({'type': 'error', 'error': str(err)})
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=1)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()

try:
    run()
except (BrokenPipeError, EOFError):
    pass
except Exception as err:
    try:
        send({'type': 'error', 'error': str(err)})
    except BrokenPipeError:
        pass
