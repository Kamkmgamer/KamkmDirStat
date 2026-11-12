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
