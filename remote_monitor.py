import grp
import gzip
import json
import os
import pwd
import queue
import shutil
import signal
import struct
import subprocess
import sys
import threading
import time

# Samples CPU, memory, GPU, network and processes on the server about once a second and
# answers requests to end a process or free the page cache. It reads /proc and never writes
# to disk. Messages use the same framing as the disk report: a 4-byte length and gzip JSON.

INTERVAL = 1.0
FIRST_INTERVAL = 0.25  # the first sample comes sooner so the page fills in right away
TOP = 60
ADMIN_GROUPS = {'sudo', 'wheel', 'admin'}
CLK = os.sysconf('SC_CLK_TCK') if 'SC_CLK_TCK' in os.sysconf_names else 100
PAGE = os.sysconf('SC_PAGE_SIZE') if 'SC_PAGE_SIZE' in os.sysconf_names else 4096
SIGNALS = {'TERM': signal.SIGTERM, 'KILL': signal.SIGKILL}
out_lock = threading.Lock()


def send(value):
    payload = gzip.compress(json.dumps(value, ensure_ascii=True, separators=(',', ':')).encode('utf-8'), compresslevel=3)
    with out_lock:
        sys.stdout.buffer.write(struct.pack('>I', len(payload)))
        sys.stdout.buffer.write(payload)
        sys.stdout.buffer.flush()


def read(path):
    with open(path, 'rb') as f:
        return f.read().decode('utf-8', 'replace')


def privilege():
    """Return root, sudo (no password needed), sudo-password, or none.

    sudo is probed only for members of an administrator group, so ordinary accounts do not
    trigger "user NOT in sudoers" reports on the server."""
    if os.geteuid() == 0:
        return 'root'
    if not shutil.which('sudo'):
        return 'none'
    names = set()
    for gid in set(os.getgroups()) | {os.getegid()}:
        try:
            names.add(grp.getgrgid(gid).gr_name)
        except KeyError:
            pass
    if not names & ADMIN_GROUPS:
        return 'none'
    try:
        probe = subprocess.run(['sudo', '-n', 'true'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        if probe.returncode == 0:
            return 'sudo'
    except (OSError, subprocess.TimeoutExpired):
        pass
    return 'sudo-password'


def cpu_times():
    cores, total = [], None
    for line in read('/proc/stat').splitlines():
        if not line.startswith('cpu'):
            break
        parts = line.split()
        values = [int(v) for v in parts[1:9]] + [0] * max(0, 8 - len(parts[1:9]))
        idle = values[3] + values[4]
        sample = (sum(values), idle, values[0] + values[1], values[2] + values[5] + values[6], values[4])
        if parts[0] == 'cpu':
            total = sample
        else:
            cores.append(sample)
    return total, cores


def busy(now, before):
    if before is None:
        return 0.0
    elapsed = now[0] - before[0]
    if elapsed <= 0:
        return 0.0
    return max(0.0, min(100.0, 100.0 * (1 - (now[1] - before[1]) / elapsed)))


def share(now, before, index):
    if before is None:
        return 0.0
    elapsed = now[0] - before[0]
    return max(0.0, min(100.0, 100.0 * (now[index] - before[index]) / elapsed)) if elapsed > 0 else 0.0


def memory():
    info = {}
    for line in read('/proc/meminfo').splitlines():
        key, _, rest = line.partition(':')
        fields = rest.split()
        if fields:
            info[key] = int(fields[0]) * 1024
    total = info.get('MemTotal', 0)
    free = info.get('MemFree', 0)
    buffers = info.get('Buffers', 0)
    cached = info.get('Cached', 0) + info.get('SReclaimable', 0) - info.get('Shmem', 0)
    available = info.get('MemAvailable', free + buffers + max(0, cached))
    swap_total = info.get('SwapTotal', 0)
    return {'total': total, 'available': available, 'used': max(0, total - available), 'free': free,
            'buffers': buffers, 'cached': max(0, cached), 'shmem': info.get('Shmem', 0),
            'dirty': info.get('Dirty', 0), 'swapTotal': swap_total,
            'swapUsed': max(0, swap_total - info.get('SwapFree', 0))}


def network():
    counters = {}
    try:
        lines = read('/proc/net/dev').splitlines()[2:]
    except OSError:
        return counters
    for line in lines:
        name, _, rest = line.partition(':')
        name, fields = name.strip(), rest.split()
        if name and name != 'lo' and len(fields) >= 9:
            counters[name] = (int(fields[0]), int(fields[8]))
    return counters


class GPUs:
    def __init__(self):
        self.nvidia = shutil.which('nvidia-smi')
        self.failures = 0
        self.retry = 0.0

    def nvidia_sample(self):
        fields = 'index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw'
        result = subprocess.run([self.nvidia, '--query-gpu=' + fields, '--format=csv,noheader,nounits'],
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=4)
        if result.returncode:
            raise OSError('nvidia-smi exited with status ' + str(result.returncode))
        gpus = []
        for line in result.stdout.decode('utf-8', 'replace').splitlines():
            parts = [p.strip() for p in line.split(',')]
            if len(parts) < 7:
                continue
            def number(text, scale=1):
                try:
                    return float(text) * scale
                except ValueError:
                    return None
            gpus.append({'vendor': 'NVIDIA', 'name': parts[1], 'util': number(parts[2]),
                         'memUsed': number(parts[3], 1 << 20), 'memTotal': number(parts[4], 1 << 20),
                         'temp': number(parts[5]), 'power': number(parts[6])})
        return gpus

    def sysfs_sample(self):
        gpus = []
        try:
            cards = sorted(n for n in os.listdir('/sys/class/drm') if n.startswith('card') and n[4:].isdigit())
        except OSError:
            return gpus
        for card in cards:
            base = '/sys/class/drm/' + card + '/device/'
            if not os.path.exists(base + 'gpu_busy_percent'):
                continue
            def value(name):
                try:
                    return float(read(base + name).strip())
                except (OSError, ValueError):
                    return None
            name = 'AMD GPU' if os.path.exists(base + 'mem_info_vram_total') else 'GPU'
            try:
                name = read(base + 'product_name').strip() or name
            except OSError:
                pass
            temp = None
            try:
                for hw in os.listdir(base + 'hwmon'):
                    t = value('hwmon/' + hw + '/temp1_input')
                    if t is not None:
                        temp = t / 1000
                        break
            except OSError:
                pass
            gpus.append({'vendor': 'AMD', 'name': name, 'util': value('gpu_busy_percent'),
                         'memUsed': value('mem_info_vram_used'), 'memTotal': value('mem_info_vram_total'),
                         'temp': temp, 'power': None})
        return gpus

    def sample(self):
        if self.nvidia and time.monotonic() >= self.retry:
            try:
                gpus = self.nvidia_sample()
                self.failures = 0
                if gpus:
                    return gpus, ''
            except (OSError, subprocess.TimeoutExpired):
                self.failures += 1
                if self.failures >= 3:
                    self.retry = time.monotonic() + 30
        gpus = self.sysfs_sample()
        if gpus:
            return gpus, ''
        if self.nvidia:
            return [], 'nvidia-smi is installed but did not report a GPU.'
        return [], 'No supported GPU found. NVIDIA GPUs need nvidia-smi; AMD GPUs are read from sysfs.'


def process_table(previous, elapsed, cores, total_memory, me):
    users = {}
    rows, current = [], {}
    for entry in os.listdir('/proc'):
        if not entry.isdigit():
            continue
        pid = int(entry)
        try:
            stat = read('/proc/' + entry + '/stat')
            uid = os.stat('/proc/' + entry).st_uid
        except OSError:
            continue
        open_paren, close_paren = stat.find('('), stat.rfind(')')
        if open_paren < 0 or close_paren < 0:
            continue
        name = stat[open_paren + 1:close_paren]
        fields = stat[close_paren + 2:].split()
        if len(fields) < 22:
            continue
        ppid, ticks, start, rss = int(fields[1]), int(fields[11]) + int(fields[12]), int(fields[19]), int(fields[21]) * PAGE
        key = (pid, start)
        current[key] = ticks
        cpu = 0.0
        if key in previous and elapsed > 0:
            cpu = 100.0 * (ticks - previous[key]) / CLK / elapsed / max(1, cores)
        if uid not in users:
            try:
                users[uid] = pwd.getpwuid(uid).pw_name
            except KeyError:
                users[uid] = str(uid)
        rows.append({'pid': pid, 'ppid': ppid, 'start': start, 'name': name, 'user': users[uid],
                     'own': uid == me, 'cpu': round(max(0.0, cpu), 2), 'rss': rss,
                     'mem': round(100.0 * rss / total_memory, 2) if total_memory else 0,
                     'kernel': pid == 2 or ppid == 2})
    guarded = protected_pids()
    # When the app runs this on its own computer, its window processes are its children too.
    launcher = os.getppid() if os.environ.get('KAMKM_LOCAL') == '1' else None
    chosen = {}
    # The busiest processes, plus this session's own so it is clear why they cannot be ended.
    busiest = sorted(rows, key=lambda r: -r['cpu'])[:TOP] + sorted(rows, key=lambda r: -r['rss'])[:TOP]
    for row in busiest + [r for r in rows if r['pid'] in guarded and r['pid'] > 1]:
        chosen[row['pid']] = row
    for row in chosen.values():
        try:
            command = read('/proc/' + str(row['pid']) + '/cmdline').replace('\0', ' ').strip()
        except OSError:
            command = ''
        row['cmd'] = command[:300]
        row['protected'] = row['pid'] in guarded or row['ppid'] == launcher
    return list(chosen.values()), len(rows), current


def protected_pids():
    """This sampler and every process above it, such as the SSH session, plus init."""
    pids, pid = {0, 1}, os.getpid()
    while pid > 1 and pid not in pids:
        pids.add(pid)
        try:
            stat = read('/proc/' + str(pid) + '/stat')
            pid = int(stat[stat.rfind(')') + 2:].split()[1])
        except (OSError, ValueError, IndexError):
            break
    return pids


def run_sudo(command, password, level):
    if level == 'root':
        argv = command
    elif level == 'sudo':
        argv = ['sudo', '-n', '--'] + command
    elif level == 'sudo-password':
        argv = ['sudo', '-S', '-p', '', '--'] + command
    else:
        raise PermissionError('This account does not have root or sudo access.')
    data = ((password or '') + '\n').encode('utf-8') if level == 'sudo-password' else None
    try:
        result = subprocess.run(argv, input=data, stdin=None if data is not None else subprocess.DEVNULL,
                                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=20)
    except subprocess.TimeoutExpired:
        raise PermissionError('The privileged command timed out.')
    if result.returncode:
        detail = result.stderr.decode('utf-8', 'replace').strip().splitlines()
        message = detail[-1][:300] if detail else 'exit status ' + str(result.returncode)
        if level != 'root' and message.startswith('sudo:'):
            message = 'sudo refused the command. Check the password and that this account may use sudo.'
        raise PermissionError(message)


def end_process(request, level):
    pid, start = request.get('pid'), request.get('start')
    sig = SIGNALS.get(request.get('signal'))
    if type(pid) is not int or type(start) is not int or sig is None:
        raise ValueError('Invalid request to end a process.')
    if pid in protected_pids():
        raise ValueError('This process is part of the system or this monitoring session and cannot be ended here.')
    try:
        stat = read('/proc/' + str(pid) + '/stat')
        fields = stat[stat.rfind(')') + 2:].split()
        if int(fields[19]) != start:
            raise ProcessLookupError
        if pid == 2 or int(fields[1]) == 2:
            raise ValueError('Kernel threads cannot be ended.')
    except (OSError, IndexError, ProcessLookupError):
        return 'The process had already exited.'
    try:
        os.kill(pid, sig)
    except ProcessLookupError:
        return 'The process had already exited.'
    except PermissionError:
        if level == 'none':
            raise PermissionError('You can only end your own processes. Connect as root or a sudo user to end this one.')
        run_sudo(['kill', '-s', request.get('signal'), str(pid)], request.get('password'), level)
    verb = 'Force-ended' if sig == signal.SIGKILL else 'Asked to end'
    return verb + ' process ' + str(pid) + '.'


def drop_caches(request, level):
    before = memory()
    run_sudo(['sh', '-c', 'sync && echo 3 > /proc/sys/vm/drop_caches'], request.get('password'), level)
    after = memory()
    freed = max(0, before['cached'] + before['buffers'] - after['cached'] - after['buffers'])
    return 'Cache freed. About ' + str(freed // (1 << 20)) + ' MiB of cached memory was released.'


def run():
    os.environ['LC_ALL'] = 'C'
    if not os.path.exists('/proc/stat') or not os.path.exists('/proc/meminfo'):
        print('SSM1 unsupported', flush=True)
        return
    print('SSM1 gzip', flush=True)
    level = privilege()
    me = os.geteuid()
    uname = os.uname()
    model = ''
    try:
        for line in read('/proc/cpuinfo').splitlines():
            if line.lower().startswith(('model name', 'hardware', 'cpu model')):
                model = line.partition(':')[2].strip()
                break
    except OSError:
        pass
    try:
        user = pwd.getpwuid(me).pw_name
    except KeyError:
        user = str(me)
    send({'type': 'hello', 'platform': 'linux', 'privilege': level, 'user': user, 'host': uname.nodename,
          'system': uname.sysname + ' ' + uname.release, 'machine': uname.machine, 'cpuModel': model})

    requests = queue.Queue()

    def reader():
        for line in sys.stdin:
            if len(line) > 8192:
                continue
            try:
                requests.put(json.loads(line))
            except ValueError:
                pass
        requests.put(None)
    threading.Thread(target=reader, daemon=True).start()

    def handle(request):
        kind, ident = request.get('type'), request.get('id')
        try:
            if kind == 'kill':
                message = end_process(request, level)
            elif kind == 'dropcaches':
                if level == 'none':
                    raise PermissionError('Freeing the cache needs root or sudo access.')
                message = drop_caches(request, level)
            else:
                raise ValueError('Unknown request.')
            send({'type': 'result', 'id': ident, 'ok': True, 'message': message})
        except (ValueError, PermissionError, OSError) as err:
            send({'type': 'result', 'id': ident, 'ok': False, 'message': str(err)})

    # Privileged commands can wait on sudo, so they run beside the sampling loop.
    def worker():
        while True:
            request = jobs.get()
            if request is None:
                return
            handle(request)
    jobs = queue.Queue()
    threading.Thread(target=worker, daemon=True).start()

    gpus = GPUs()
    cpu_before, cores_before = cpu_times()
    net_before, procs_before = network(), {}
    last = time.monotonic()
    _, _, procs_before = process_table({}, 0, 1, 0, me)
    deadline = last + FIRST_INTERVAL
    while True:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            try:
                request = requests.get(timeout=remaining)
            except queue.Empty:
                break
            if request is None or request.get('type') == 'close':
                jobs.put(None)
                return
            if isinstance(request, dict):
                jobs.put(request)
        now = time.monotonic()
        elapsed, last = now - last, now
        deadline = now + INTERVAL
        cpu_now, cores_now = cpu_times()
        net_now = network()
        mem = memory()
        rows, count, procs_before = process_table(procs_before, elapsed, max(1, len(cores_now)), mem['total'], me)
        gpu_list, gpu_note = gpus.sample()
        interfaces = []
        for name, (rx, tx) in sorted(net_now.items()):
            old = net_before.get(name)
            rate = lambda a, b: max(0, a - b) / elapsed if old and elapsed > 0 else 0
            interfaces.append({'name': name, 'rx': rate(rx, old[0] if old else 0), 'tx': rate(tx, old[1] if old else 0),
                               'rxTotal': rx, 'txTotal': tx})
        try:
            load = list(os.getloadavg())
        except OSError:
            load = []
        try:
            uptime = float(read('/proc/uptime').split()[0])
        except (OSError, ValueError, IndexError):
            uptime = 0
        cores = [round(busy(c, cores_before[i] if i < len(cores_before) else None), 1) for i, c in enumerate(cores_now)]
        send({'type': 'sample', 'time': time.time(), 'uptime': uptime, 'load': load,
              'cpu': {'total': round(busy(cpu_now, cpu_before), 1), 'user': round(share(cpu_now, cpu_before, 2), 1),
                      'system': round(share(cpu_now, cpu_before, 3), 1), 'iowait': round(share(cpu_now, cpu_before, 4), 1),
                      'cores': cores},
              'memory': mem, 'gpus': gpu_list, 'gpuNote': gpu_note, 'network': interfaces,
              'processes': rows, 'processCount': count})
        cpu_before, cores_before, net_before = cpu_now, cores_now, net_now


def stop(signum, frame):
    raise SystemExit(0)


for _name in ('SIGHUP', 'SIGTERM', 'SIGINT', 'SIGPIPE'):
    if hasattr(signal, _name):
        signal.signal(getattr(signal, _name), stop if _name != 'SIGPIPE' else signal.SIG_DFL)

try:
    run()
except (BrokenPipeError, EOFError, SystemExit):
    pass
except Exception as err:
    try:
        send({'type': 'error', 'error': str(err)})
    except (BrokenPipeError, OSError):
        pass
