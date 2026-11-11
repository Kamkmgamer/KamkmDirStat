import collections
import gzip
import json
import os
import shutil
import signal
import struct
import subprocess
import sys
import threading
import time

# This process owns the scan and detailed results for one SSH session. Nothing is
# installed or written to disk. Requests after completion retrieve one folder.
class Node:
    __slots__ = ('name', 'parent', 'directory', 'size', 'files', 'children', 'incomplete', 'id')
    def __init__(self, name, parent=None, directory=False, size=0):
        self.name, self.parent, self.directory, self.size = name, parent, directory, size
        self.files = 0 if directory else 1
        self.children, self.incomplete, self.id = [], False, 0


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
        entries.append({'name': child.name, 'size': child.size, 'dir': child.directory,
                        'id': child.id, 'files': child.files, 'incomplete': child.incomplete})
    map_root = {'n': node.name, 's': node.size, 'd': True, 'i': node.id}
    queue = collections.deque([(node, map_root)])
    threshold, budget = max(node.size // 4000, 1), 12000
    while queue:
        current, mapped = queue.popleft()
        children, other = [], 0
        for child in current.children:
            if child.size < threshold or budget <= 0:
                other += child.size
                continue
            budget -= 1
            item = {'n': child.name, 's': child.size}
            if child.directory:
                item.update(d=True, i=child.id)
                if child.children:
                    queue.append((child, item))
            children.append(item)
        if children:
            mapped['c'] = children
        if other:
            mapped['o'] = other
    return {'id': index, 'trail': trail[::-1], 'size': node.size, 'files': node.files,
            'items': len(node.children), 'incomplete': node.incomplete, 'entries': entries,
            'moreCount': max(0, len(node.children) - 500),
            'moreSize': sum(child.size for child in node.children[500:]), 'map': map_root}


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
    root = Node(root_path, directory=True)
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
    def apply(raw):
        nonlocal bad
        try:
            kind = raw[:1]
            fields = raw.split(b' ', 4 if kind == b'h' else 3)
            if kind == b'h':
                _, inode, blocks, depth, name = fields
            else:
                _, blocks, depth, name = fields
                inode = None
            blocks, depth = int(blocks), int(depth)
            if blocks < 0 or blocks > (2**63 - 1) // 512 or depth < 0 or depth > 4096 or not name or (depth > 0 and b'/' in name):
                raise ValueError('invalid find record')
            progress['entries'] += 1
            size = blocks * 512
            if depth == 0:
                if kind == b'd':
                    root.size += size
                    progress['bytes'] += size
                return
            if depth > len(stack):
                raise ValueError('missing parent directory')
            parent, relative = stack[depth - 1], rels[depth - 1]
            del stack[depth:]
            del rels[depth:]
            if kind == b'l':
                return
            name = name.decode('utf-8', 'replace')
            if inode is not None:
                if inode in seen:
                    size = 0
                else:
                    seen.add(inode)
            node = Node(name, parent, kind == b'd', size)
            parent.children.append(node)
            if node.directory:
                node.id = len(dirs)
                dirs.append(node)
                relative = relative + '/' + name if relative else name
                stack.append(node)
                rels.append(relative)
                by_path[relative] = node
                progress['directories'] += 1
            else:
                parent.files += 1
                parent.size += size
                progress['files'] += 1
            progress['bytes'] += size
        except (ValueError, IndexError):
            bad += 1
    try:
        pending = b''
        while True:
            chunk = proc.stdout.read1(256 * 1024)
            if not chunk:
                break
            records = (pending + chunk).split(b'\0')
            pending = records.pop()
            for record in records:
                apply(record)
            now = time.monotonic()
            if now - last_report >= 0.75:
                send({'type': 'progress', 'progress': progress})
                last_report = now
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
            node.children.sort(key=lambda n: (-n.size, n.name))
            if node.parent is not None:
                node.parent.size += node.size
                node.parent.files += node.files
        send({'type': 'done', 'view': folder_view(dirs, 0), 'progress': progress,
              'errors': errors, 'errorCount': error_count})
        for line in sys.stdin:
            if len(line) > 4096:
                raise ValueError('Report request is too large.')
            request = json.loads(line)
            if request.get('type') == 'close':
                return
            try:
                send({'type': 'view', 'view': folder_view(dirs, request.get('id'))})
            except ValueError as err:
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
