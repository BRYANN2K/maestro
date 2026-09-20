#!/usr/bin/env python3
"""Local, offline spec lifecycle. Python 3.10+. Evidence is an attestation, not a test runner."""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

VERSION = 1
CHANGE_VERSIONS = (1, 2)
PLAN_PHASES = ('apply', 'check', 'docs')
ACTIVE_JOB_STATUSES = {'starting', 'running', 'waiting', 'unknown', 'correction'}
JOB_STATUSES = ACTIVE_JOB_STATUSES | {'queued', 'returned', 'failed', 'cancelled'}
SLUG = re.compile(r'^[a-z0-9]+(?:-[a-z0-9]+)*$')
MARKER = '<!-- spec-workflow:start -->'
END = '<!-- spec-workflow:end -->'
GUIDANCE = '''<!-- spec-workflow:start -->
# AGENTS.md

## Mission

Carry the project through to the requested outcome within the authorized scope.
Use available context to resolve routine details and keep working without repeated confirmations.

Preserve existing work. Never present an assumption, planned action, or unexecuted check as a verified result.

## Collaboration

Evaluate the user's proposals independently. Point out errors, contradictions, or weak approaches by explaining the facts, consequences, and a concrete alternative.

Do not manufacture disagreement. Distinguish facts, hypotheses, and preferences. Revise your position when evidence changes and respect the user's informed choices.

Ask for clarification when ambiguity materially changes the outcome, scope, or a decision that is difficult to reverse. For a minor, reversible choice, state an assumption and proceed.

While awaiting an answer, continue independent work. Do not request authorization that has already been granted. Silence does not constitute approval.

## Communication

Respond in the user's language and keep cognitive load low:

- Lead with the outcome, recommendation, or decision needed.
- Use short paragraphs and easy-to-scan lists.
- Reserve bold text for decisive information.
- Recommend an option and briefly explain why.
- Group necessary questions without creating repeated interruptions.
- Put lengthy details in an accessible document.
- When resuming, briefly recall the goal, progress, and next action.

Adapt depth to the request. Brevity must not hide uncertainty, a concrete risk, or incomplete work.

Do not automatically turn thinking aloud into an execution plan. If the user expresses overload, help identify one simple next action.

## Understand the project

Read applicable instructions and inspect repository state before making changes.

Use:
- `.workflow/project.md` for project intent and orientation;
- `.workflow/config.json` for extension configuration;
- `.workflow/specs/` for accepted behaviors;
- `.workflow/changes/<id>/` for the current change.

Load only documents relevant to the task. Verify potentially outdated information against the code.

When a document contradicts the implementation, report the discrepancy. Do not silently change the contract to justify existing behavior.

## Use the workflow

Follow the `stip-*` skill procedures without duplicating them here.

### stip-bootstrap

Initialize the workflow for a new project or adopt it in an existing one. Preserve useful conventions and instructions.

Map relevant domains and existing assets. Distinguish established, inferred, incomplete, missing, and not-applicable findings. Missing documentation does not prove that a practice is absent.

Record project commands and constraints from verified facts. Do not repeat bootstrap for every change.

### stip-explore

Explore intent with the user. Bring in enabled extensions that are relevant to the actual change.

Examine their domain questions, existing project foundations, and gaps worth addressing. An available extension is not automatically mandatory. A cloud-only change does not require a design process.

### stip-validate

Turn exploration into a reviewable, editable contract. Explain expected behaviors, boundaries, and acceptance criteria clearly.

Allow the user to request changes in natural language. Record explicit approval of the current version before apply.

### stip-apply

Implement the approved contract. Continue necessary corrections and verification within that scope.

If a discovery requires changing the contract, explain the impact and return to validation before implementing that change.

### stip-check

Verify every acceptance criterion using current evidence. Distinguish passed, failed, and unverified results.

Do not declare the change compliant while any requirement remains unmet or lacks sufficient evidence.

### stip-docs

Update affected documentation based on verified behavior. Adapt it to the relevant readers and their usage, maintenance, and operational needs.

### stip-archive

Close the change under the skill's conditions: promote the accepted specification, archive the change, and create a scoped local commit.

Closure does not authorize publication or deployment.

## Verification and review

Choose checks based on acceptance criteria, affected paths, and concrete risks. Run the checks required by the project.

After they pass, broaden or repeat checks only when a change, failure, or remaining uncertainty justifies it.

For important decisions, look for counterexamples, weak assumptions, and failure scenarios.

When delegation is authorized, assign bounded, independent tasks that improve quality or completion time. Define ownership and preserve concurrent changes.

Give reviewers the material to examine without prescribing a conclusion. Agreement among agents does not replace evidence.

## Handling blockers

Explain precisely what prevents progress and what would unblock it. Continue any independent work that remains possible.

If a skill instruction requires stopping or asking for confirmation, identify the file and rule, then distinguish the explicit requirement from your interpretation.

Respect higher-priority instructions and the authorizations actually granted.
<!-- spec-workflow:end -->
'''

class WorkflowError(Exception):
    pass

def require(condition, message):
    if not condition:
        raise WorkflowError(message)

def now():
    return datetime.now(timezone.utc).isoformat()

def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()

def read_json(path):
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError) as exc:
        raise WorkflowError(f'Invalid JSON at {path}: {exc}') from exc

def write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix='.workflow-write-')
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(content)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)

def write_json(path, data):
    write(path, json.dumps(data, ensure_ascii=False, indent=2) + '\n')

def safe(root, relative):
    p = Path(relative)
    require(not p.is_absolute() and '..' not in p.parts, f'Unsafe relative path: {relative}')
    current = root
    for part in p.parts:
        current = current / part
        require(not current.is_symlink(), f'Symlink not allowed in workflow path: {current}')
    return current

def slug(value):
    require(bool(SLUG.fullmatch(value)), f'Invalid id: {value}')
    return value

def git(root, *args, check=True):
    p = subprocess.run(['git', '-C', str(root), *args], capture_output=True)
    if check and p.returncode:
        raise WorkflowError(p.stderr.decode(errors='replace').strip() or 'Git command failed')
    return p

def git_root(root):
    p = git(root, 'rev-parse', '--show-toplevel', check=False)
    require(p.returncode == 0 and Path(p.stdout.decode().strip()).resolve() == root,
            'Use the Git repository root; initialize Git first for a new project.')

def snapshot(root):
    git_root(root)
    output = git(root, 'ls-files', '-z', '--cached', '--others', '--exclude-standard').stdout
    result = {}
    for raw in sorted(set(output.split(b'\0')) - {b''}):
        name = os.fsdecode(raw)
        if name == '.workflow' or name.startswith('.workflow/'):
            continue
        p = root / name
        if p.is_symlink():
            value = 'symlink:' + os.readlink(p)
        elif not p.exists():
            continue  # Absence is stable before and after Git commits a deletion.
        elif p.is_file():
            value = hashlib.sha256(p.read_bytes()).hexdigest() + ':' + oct(p.stat().st_mode & 0o111)
        else:
            raise WorkflowError(f'Unsupported tracked entry (e.g. submodule): {name}')
        result[name] = value
    return result

def changed(a, b):
    return {key for key in a.keys() | b.keys() if a.get(key) != b.get(key)}

def head(root):
    p = git(root, 'rev-parse', '--verify', 'HEAD', check=False)
    return p.stdout.decode().strip() if p.returncode == 0 else None

def dirty(root):
    # Existing modifications, including untracked files, must not enter a feature commit.
    names = set()
    for args in [('diff', '--name-only', '-z'), ('diff', '--cached', '--name-only', '-z'),
                 ('ls-files', '--others', '--exclude-standard', '-z')]:
        names.update(os.fsdecode(x) for x in git(root, *args).stdout.split(b'\0') if x)
    return sorted(names)

def config(root):
    c = read_json(safe(root, '.workflow/config.json'))
    require(c.get('schema_version') == VERSION and isinstance(c.get('extensions'), dict),
            'Unsupported config schema; expected schema_version=1 and extensions object.')
    require(c.get('settings', {}).get('require_user_approval', True) is True, 'User approval cannot be disabled in core v1.')
    return c

def change_dir(root, name):
    return safe(root, f'.workflow/changes/{slug(name)}')

def load(root, name):
    config(root)
    p = change_dir(root, name)
    s = read_json(safe(p, 'state.json'))
    require(s.get('schema_version') in CHANGE_VERSIONS and s.get('id') == name, 'Invalid change state.')
    return p, s

def save(p, s, phase):
    s['phase'] = phase
    s['updated_at'] = now()
    write_json(p / 'state.json', s)

def extension_data(root, identifiers):
    c = config(root)
    result = {}
    for name in identifiers:
        slug(name)
        entry = c['extensions'].get(name)
        require(isinstance(entry, dict) and entry.get('enabled') is True,
                f'Extension not enabled: {name}')
        base = safe(root, entry.get('path', ''))
        require(base != root, 'Extension path must identify a directory.')
        manifest = read_json(safe(root, str(base.relative_to(root) / 'extension.json')))
        require(manifest.get('schema_version') == 1 and manifest.get('id') == name,
                f'Invalid extension manifest: {name}')
        require(isinstance(manifest.get('explore'), str), f'{name} needs an explore reference.')
        refs = {}
        for key in ('explore', 'apply', 'check', 'docs'):
            if key in manifest:
                rel = manifest[key]
                require(isinstance(rel, str) and bool(rel), 'Invalid extension reference.')
                p = safe(base, rel)
                require(p.is_file(), f'Missing extension reference: {p}')
                refs[key] = {'path': str(p.relative_to(root)), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()}
        result[name] = {'manifest': manifest, 'references': refs}
    return result

def contract(root, p, s):
    files = {}
    for name in ('proposal.md', 'spec.md', 'tasks.md'):
        q = safe(p, name)
        if q.exists():
            files[name] = q.read_text()
    value = {'files': files, 'target': s['target'],
             'extensions': extension_data(root, s['extensions'])}
    if s['schema_version'] == 2:
        value['schema_version'] = 2
        value['execution_plan'] = validate_task_plan(root, p, s)
    return digest(value)

def criteria(p):
    text = (p / 'spec.md').read_text()
    found = re.findall(r'^- (AC-[1-9][0-9]*):\s*(\S.*)$', text, re.M)
    require(found, 'Spec requires acceptance criteria: - AC-1: observable behavior')
    ids = [key for key, _ in found]
    require(len(set(ids)) == len(ids), 'Duplicate acceptance criterion id.')
    require(not re.search(r'\b(TODO|TBD|FIXME)\b|\{\{', text), 'Unresolved template placeholder in spec.')
    require((p / 'proposal.md').read_text().strip(), 'Empty proposal.')
    return ids

def task_path(root, value, writable=False):
    require(isinstance(value, str) and bool(value) and value == value.strip(), 'Task paths must be nonempty relative paths.')
    require('\\' not in value and not any(x in value for x in '*?[]\x00\n\r'), 'Task paths must be literal paths, not globs.')
    path = Path(value)
    require(path.as_posix() == value and value != '.', 'Task paths must use canonical relative paths.')
    safe(root, value)
    require(path.parts[0].casefold() != '.git', 'Tasks cannot access Git internals.')
    require(not writable or path.parts[0].casefold() != '.workflow', 'Workers cannot own workflow metadata.')
    return value

def validate_task_plan(root, p, s, data=None):
    """Validate the semantic task contract; progress and model settings live elsewhere."""
    if data is None:
        data = read_json(safe(p, 'execution-plan.json'))
    require(isinstance(data, dict) and set(data) == {'version', 'tasks'} and type(data.get('version')) is int and data['version'] == 1,
            'Execution plan expects version=1 and tasks only.')
    tasks = data['tasks']
    require(isinstance(tasks, list) and 0 < len(tasks) <= 200, 'Execution plan requires 1 to 200 tasks.')
    criterion_ids = set(criteria(p))
    by_id = {}
    normalized = []
    fields = {'id', 'role', 'phase', 'criteria', 'depends_on', 'write_paths', 'objective'}
    optional = {'read_paths', 'expected_output'}
    for task in tasks:
        require(isinstance(task, dict) and fields <= task.keys() and task.keys() <= fields | optional,
                'Task requires id, role, phase, criteria, depends_on, write_paths, objective; only read_paths and expected_output are optional.')
        identifier, role = task['id'], task['role']
        require(isinstance(identifier, str) and isinstance(role, str), 'Task id and role must be strings.')
        slug(identifier); slug(role)
        require(identifier not in by_id, f'Duplicate task id: {identifier}')
        require(role != 'discovery', 'Discovery stays with the main coordinator; use research for a bounded helper task.')
        require(task['phase'] in PLAN_PHASES, f'Invalid task phase for {identifier}: use apply, check or docs.')
        require(isinstance(task['objective'], str) and bool(task['objective'].strip()), f'Task {identifier} needs an objective.')
        require(isinstance(task['criteria'], list) and task['criteria'] and all(isinstance(x, str) for x in task['criteria'])
                and len(set(task['criteria'])) == len(task['criteria']) and set(task['criteria']) <= criterion_ids,
                f'Task {identifier} must reference existing unique acceptance criteria.')
        dependencies = task['depends_on']
        require(isinstance(dependencies, list) and all(isinstance(x, str) for x in dependencies)
                and len(set(dependencies)) == len(dependencies) and identifier not in dependencies,
                f'Task {identifier} has invalid dependencies.')
        for key in ('write_paths', 'read_paths'):
            paths = task.get(key, [])
            require(isinstance(paths, list) and all(isinstance(x, str) for x in paths) and len(paths) == len(set(paths)),
                    f'Task {identifier} needs a unique {key} array.')
            for path in paths:
                task_path(root, path, writable=key == 'write_paths')
        if 'expected_output' in task:
            require(isinstance(task['expected_output'], str) and bool(task['expected_output'].strip()), 'Expected output must be nonempty text.')
        normalized_task = dict(task)
        for key in ('criteria', 'depends_on', 'write_paths'):
            normalized_task[key] = sorted(task[key])
        normalized_task['read_paths'] = sorted(task.get('read_paths', []))
        normalized.append(normalized_task)
        by_id[identifier] = normalized_task
    require(set().union(*(set(t['criteria']) for t in tasks)) == criterion_ids, 'Task plan must cover every acceptance criterion.')
    ancestors = {}
    visiting = set()
    def visit(identifier):
        require(identifier not in visiting, 'Execution plan contains a dependency cycle.')
        if identifier in ancestors:
            return ancestors[identifier]
        visiting.add(identifier)
        result = set()
        task = by_id[identifier]
        for dependency in task['depends_on']:
            require(dependency in by_id, f'Unknown dependency: {dependency}')
            require(PLAN_PHASES.index(by_id[dependency]['phase']) <= PLAN_PHASES.index(task['phase']),
                    f'Task {identifier} depends on a later phase: {dependency}')
            result.add(dependency)
            result.update(visit(dependency))
        visiting.remove(identifier)
        ancestors[identifier] = result
        return result
    for identifier in by_id:
        visit(identifier)
    for index, task in enumerate(normalized):
        for other in normalized[index + 1:]:
            if task['phase'] != other['phase'] or task['id'] in ancestors[other['id']] or other['id'] in ancestors[task['id']]:
                continue
            for left in task['write_paths']:
                for right in other['write_paths']:
                    a, b = left.casefold(), right.casefold()
                    require(not (a == b or a.startswith(b + '/') or b.startswith(a + '/')),
                            f'Independent tasks {task["id"]} and {other["id"]} have overlapping write ownership: {left}, {right}')
    return {'version': 1, 'tasks': sorted(normalized, key=lambda t: t['id'])}

def runtime_state(root, p, s, required=False):
    path = safe(root, f'.workflow/.runtime/{s["id"]}/state.json')
    if not path.exists():
        require(not required, 'Orchestration runtime state is missing; reconcile native sessions before finalizing.')
        return None
    data = read_json(path)
    require(isinstance(data, dict) and type(data.get('version')) is int and data.get('version') == 1
            and data.get('change_id') == s['id'] and isinstance(data.get('jobs'), list), 'Invalid orchestration runtime state.')
    tasks = {t['id']: t for t in validate_task_plan(root, p, s)['tasks']}
    seen = set()
    sessions = {}
    for job in data['jobs']:
        require(isinstance(job, dict), 'Invalid worker record.')
        identifier, attempt = job.get('task_id'), job.get('attempt')
        require(isinstance(identifier, str) and identifier in tasks and type(attempt) is int and attempt >= 1,
                'Worker record needs a known task_id and a positive attempt.')
        require((identifier, attempt) not in seen, 'Duplicate task attempt in orchestration runtime state.')
        seen.add((identifier, attempt))
        require(job.get('status') in JOB_STATUSES and job.get('acceptance') in {'pending', 'accepted', 'rejected'},
                'Invalid worker status or acceptance.')
        session = job.get('session_id')
        require(session is None or isinstance(session, str) and bool(session.strip()), 'Invalid native worker session id.')
        if session:
            require(session not in sessions or sessions[session] == identifier, 'A native session cannot own unrelated tasks.')
            sessions[session] = identifier
        if job['acceptance'] == 'accepted':
            require(job['status'] == 'returned' and session, 'An accepted contribution requires a returned native session.')
    return data

def quiescent(root, p, s, phases=()):
    if s['schema_version'] != 2:
        return None
    data = runtime_state(root, p, s, required=True)
    require(data.get('contract_digest') == contract(root, p, s), 'Worker registry belongs to a different contract; reconcile it before finalizing.')
    require(not any(j['status'] in ACTIVE_JOB_STATUSES for j in data['jobs']),
            'Workers are active or their outcome is unknown; reconcile native sessions before finalizing.')
    latest = {}
    for job in data['jobs']:
        if job['attempt'] > latest.get(job['task_id'], {}).get('attempt', 0):
            latest[job['task_id']] = job
    for task in validate_task_plan(root, p, s)['tasks']:
        if task['phase'] in phases:
            require(latest.get(task['id'], {}).get('acceptance') == 'accepted',
                    f'Task {task["id"]} has no accepted contribution for its latest attempt.')
    return data

def execution_summary(root, p, s, registry, subject):
    """Export a bounded public provenance projection, never the private registry."""
    def text(value, field, limit=512):
        require(value is None or isinstance(value, str) and len(value.encode('utf-8')) <= limit
                and not any(c in value for c in '\x00\n\r'), f'Invalid or oversized execution summary {field}.')
        return value

    def model(value):
        if isinstance(value, dict):
            identifier = text(value.get('id'), 'model id')
            provider = text(value.get('providerID'), 'model provider')
            variant = text(value.get('variant'), 'model variant')
            value = ((provider + '/' if provider else '') + identifier + ('#' + variant if variant else '')) if identifier else None
        elif not isinstance(value, str):
            value = None
        return text(None if value in ('', 'inherit') else value, 'model reference')

    latest = {}
    for job in registry['jobs']:
        if job['attempt'] > latest.get(job['task_id'], {}).get('attempt', 0):
            latest[job['task_id']] = job
    tasks = []
    for task in validate_task_plan(root, p, s)['tasks']:
        job = latest[task['id']]
        profile = job.get('profile') if isinstance(job.get('profile'), dict) else {}
        effort = profile.get('effort')
        tasks.append({'task_id': task['id'], 'role': task['role'], 'phase': task['phase'],
                      'criteria': task['criteria'], 'attempt': job['attempt'],
                      'session_id': text(job['session_id'], 'session id'),
                      'status': job['status'], 'acceptance': job['acceptance'],
                      'requested_model': model(profile.get('model')),
                      'actual_model': model(job.get('actual_model')),
                      'effort': text(None if effort in ('inherit', 'model default') else effort, 'effort', 64),
                      'fast': profile.get('fast') if type(profile.get('fast')) is bool else None,
                      'native_outcome': job.get('native_outcome') if job.get('native_outcome') in ('succeeded', 'failed', 'interrupted') else None})
    summary = {'version': 1, 'change_id': s['id'], 'contract_digest': registry['contract_digest'],
               'subject_digest': digest(subject), 'archived_at': now(), 'tasks': tasks}
    require(len((json.dumps(summary, ensure_ascii=False, indent=2) + '\n').encode('utf-8')) <= 256 * 1024,
            'Execution summary exceeds 256 KiB; reconcile the public provenance before archive.')
    return summary

def plan(root, args):
    p, s = load(root, args.id)
    if not args.file:
        require(s['schema_version'] == 2, 'This change uses schema v1; enable orchestration with plan --file before approval.')
        return {'change': s['id'], 'schema_version': 2, 'plan': validate_task_plan(root, p, s)}
    require(s['schema_version'] == 2 or not (s.get('approval') or 'baseline' in s) or args.migrate,
            'An approved v1 change needs explicit --migrate; this invalidates its approval.')
    value = read_json(Path(args.file)) if args.file != '-' else json.load(sys.stdin)
    validated = validate_task_plan(root, p, s, value)
    runtime_path = safe(root, f'.workflow/.runtime/{s["id"]}/state.json')
    if runtime_path.exists():
        existing = runtime_state(root, p, s)
        require(not any(j['status'] in ACTIVE_JOB_STATUSES for j in existing['jobs']), 'Stop and reconcile workers before changing their contract.')
    old_plan = safe(p, 'execution-plan.json')
    before = old_plan.read_bytes() if old_plan.exists() else None
    state_before = (p / 'state.json').read_bytes()
    try:
        write_json(old_plan, validated)
        s['schema_version'] = 2
        for key in ('approval', 'check', 'documentation'):
            s.pop(key, None)
        save(p, s, 'draft')
    except Exception:
        if before is None:
            old_plan.unlink(missing_ok=True)
        else:
            write(old_plan, before.decode())
        write(p / 'state.json', state_before.decode())
        raise
    return {'change': s['id'], 'schema_version': 2, 'phase': 'draft', 'plan': validated,
            'note': 'Review and explicitly approve the task contract before apply; any existing source baseline was preserved.'}

def status(root, args):
    p, s = load(root, args.id)
    value = contract(root, p, s)
    result = dict(s) if not args.compact else {
        key: s[key] for key in ('schema_version', 'id', 'target', 'extensions', 'phase', 'updated_at') if key in s}
    result['approval_current'] = s.get('approval', {}).get('contract_digest') == value
    result['contract_digest'] = value
    if s['schema_version'] == 2:
        result['plan'] = validate_task_plan(root, p, s)
        try:
            runtime = runtime_state(root, p, s)
            result['runtime'] = None if runtime is None else {
                'contract_current': runtime.get('contract_digest') == value,
                'jobs': [{key: j[key] for key in ('task_id', 'session_id', 'status', 'acceptance', 'attempt') if key in j}
                         for j in runtime['jobs']]}
        except WorkflowError as exc:
            result['runtime'] = {'error': str(exc)}
    return result

def approved(root, p, s):
    require(s.get('approval', {}).get('contract_digest') == contract(root, p, s),
            'Contract or selected extension changed. Validate and obtain user approval again.')

def bootstrap(root, args):
    safe(root, 'AGENTS.md')
    for name in ('project.md', 'config.json', 'specs', 'changes', 'archive'):
        safe(root, '.workflow/' + name)
    cfg = root / '.workflow/config.json'
    if cfg.exists():
        config(root)
    else:
        write_json(cfg, {'schema_version': 1, 'extensions': {}, 'settings': {'require_user_approval': True}})
    for name in ('specs', 'changes', 'archive'):
        (root / '.workflow' / name).mkdir(parents=True, exist_ok=True)
    project = root / '.workflow/project.md'
    if not project.exists():
        write(project, '# Project\n\n## Intent\n\nNot assessed yet.\n\n## Project map\n\n'
              '| Area | Status | Evidence |\n|---|---|---|\n\n'
              'Use established, inferred, incomplete, missing, or not-applicable.\n'
              'Record actual paths and observations; code is not proof of desired behavior.\n')
    agents = root / 'AGENTS.md'
    existing = agents.read_text() if agents.exists() else ''
    require(existing.count(MARKER) == existing.count(END) and existing.count(MARKER) <= 1,
            'Malformed existing workflow block in AGENTS.md; repair it explicitly.')
    if MARKER not in existing:
        write(agents, existing.rstrip() + ('\n\n' if existing.strip() else '') + GUIDANCE)
    return {'status': 'bootstrapped', 'assessment': 'Agent must inspect and populate project.md; no maturity inferred.'}

def explore(root, args):
    config(root)
    p = change_dir(root, args.id)
    require(not p.exists(), 'Change already exists; edit its proposal or spec to continue exploration.')
    require(not safe(root, f'.workflow/archive/{args.id}').exists(), 'Id already archived; use a new change id.')
    selected = list(dict.fromkeys(args.extension))
    ext = extension_data(root, selected)
    target = slug(args.target or args.id)
    current = safe(root, f'.workflow/specs/{target}.md')
    proposal = '# ' + (args.title or args.id) + '\n\n## Problem\n\n## Scope\n\n## Decisions and open questions\n'
    write(p / 'proposal.md', proposal)
    write(p / 'spec.md', current.read_text() if current.exists() else '# ' + (args.title or args.id) + '\n\n## Intent\n\n## Acceptance criteria\n')
    write(p / 'evidence.md', '# Evidence\n\nNo verification recorded.\n')
    s = {'schema_version': 1, 'id': args.id, 'target': target, 'extensions': selected,
         'target_before': hashlib.sha256(current.read_bytes()).hexdigest() if current.exists() else None,
         'created_at': now()}
    save(p, s, 'exploring')
    return {'change': args.id, 'phase': 'exploring', 'extensions': ext}

def select(root, args):
    p, s = load(root, args.id)
    selected = list(dict.fromkeys(args.extension))
    result = extension_data(root, selected)
    s['extensions'] = selected
    for key in ('approval', 'check', 'documentation'):
        s.pop(key, None)
    save(p, s, 'exploring')
    return {'phase': 'exploring', 'extensions': result}

def validate(root, args):
    p, s = load(root, args.id)
    ids = criteria(p)
    value = contract(root, p, s)
    if s.get('approval', {}).get('contract_digest') != value:
        for key in ('approval', 'check', 'documentation'):
            s.pop(key, None)
        save(p, s, 'draft')
    return {'criteria': ids, 'contract_digest': value, 'phase': s['phase'],
            'semantic_review': 'User and agent must review meaning, scope and unresolved decisions.'}

def approve(root, args):
    require(bool(args.by.strip()), 'Approval attribution is required.')
    require(args.ack_user_approval, 'Record approval only after explicit user agreement to this contract.')
    p, s = load(root, args.id)
    criteria(p)
    value = contract(root, p, s)
    expected = getattr(args, 'expected_digest', None)
    require(expected is None or expected == value, 'Contract changed since it was displayed. Reload and review before approval.')
    s['approval'] = {'contract_digest': value, 'by': args.by, 'recorded_at': now()}
    for key in ('check', 'documentation'):
        s.pop(key, None)
    save(p, s, 'approved')
    return {'phase': 'approved', 'approval': s['approval']}

def start(root, args):
    p, s = load(root, args.id)
    require(s['phase'] in ('approved', 'applying', 'checked', 'documented'), 'Approval is required before apply.')
    approved(root, p, s)
    if 'baseline' not in s:
        s['baseline'] = snapshot(root)
        s['preexisting_dirty'] = dirty(root)
        s['base_head'] = head(root)
    s.pop('check', None)
    s.pop('documentation', None)
    save(p, s, 'applying')
    return {'phase': 'applying', 'extensions': extension_data(root, s['extensions'])}

def check(root, args):
    p, s = load(root, args.id)
    require(s['phase'] in ('applying', 'checked', 'documented'), 'Start apply first.')
    approved(root, p, s)
    quiescent(root, p, s, ('apply', 'check'))
    ids = criteria(p)
    report = json.load(sys.stdin) if args.results == '-' else read_json(Path(args.results))
    subject = snapshot(root)
    require(report.get('subject_digest') == digest(subject), 'Evidence refers to a different working tree.')
    entries = report.get('criteria')
    require(isinstance(entries, list), 'Expected criteria array.')
    require(all(isinstance(x, dict) for x in entries), 'Invalid criterion record.')
    require(len(entries) == len(ids) and {x.get('id') for x in entries} == set(ids), 'Report must cover every criterion exactly once.')
    allowed = {'passed', 'failed', 'unverified', 'not-applicable'}
    for item in entries:
        require(item.get('status') in allowed and isinstance(item.get('evidence'), str)
                and bool(item['evidence'].strip()), 'Every result needs a status and inspectable evidence or gap explanation.')
    s['check'] = {'subject': subject, 'report': report, 'at': now()}
    s.pop('documentation', None)
    passed = all(x['status'] == 'passed' for x in entries)
    write(p / 'evidence.md', '# Evidence\n\nSubject: `' + digest(subject) + '`\n\n' +
          '\n'.join(f"- {x['id']}: {x['status']} — {x['evidence']}" for x in entries) + '\n')
    save(p, s, 'checked' if passed else 'applying')
    return {'phase': s['phase'], 'all_passed': passed, 'note': 'Report consistency verified; evidence truth requires actual observations.'}

def docs(root, args):
    p, s = load(root, args.id)
    require(s['phase'] == 'checked', 'A successful check is required before documenting completion.')
    approved(root, p, s)
    quiescent(root, p, s, PLAN_PHASES)
    require(bool(args.summary.strip()), 'Documentation evidence summary is required.')
    current = snapshot(root)
    differences = changed(s['check']['subject'], current)
    declared = set(args.paths)
    require(all(not Path(x).is_absolute() and '..' not in Path(x).parts and Path(x).suffix.lower() in ('.md', '.rst', '.txt') for x in declared),
            'Documentation paths must be relative Markdown, reStructuredText or text files.')
    require(differences <= declared, 'Non-documentation or undeclared changes after check; run check again.')
    s['documentation'] = {'subject': current, 'paths': sorted(declared), 'summary': args.summary, 'at': now()}
    save(p, s, 'documented')
    return {'phase': 'documented', 'documentation': s['documentation']}

def archive(root, args):
    p, s = load(root, args.id)
    require(s['phase'] == 'documented', 'Check and documentation must complete before archive.')
    approved(root, p, s)
    registry = quiescent(root, p, s, PLAN_PHASES)
    current = snapshot(root)
    require(current == s['documentation']['subject'], 'Working tree changed after verification/documentation.')
    require(head(root) == s['base_head'], 'HEAD changed during this change. Reconcile the baseline explicitly before archive.')
    require(not git(root, 'diff', '--cached', '--name-only').stdout.strip(), 'Git index contains staged work; preserve it and archive later.')
    source_paths = changed(s['baseline'], current)
    requested = set(args.paths)
    require(requested == source_paths, 'Commit paths must exactly match changes since apply: ' + ', '.join(sorted(source_paths)))
    require(not source_paths.intersection(s['preexisting_dirty']), 'A selected file already contained work before apply; separate it before automatic archive.')
    require(all(not Path(x).is_absolute() and '..' not in Path(x).parts for x in requested), 'Unsafe commit path.')
    target = safe(root, f".workflow/specs/{slug(s['target'])}.md")
    before = target.read_bytes() if target.exists() else None
    require((hashlib.sha256(before).hexdigest() if before is not None else None) == s['target_before'],
            'Current accepted spec changed concurrently; reconcile before archive.')
    dest = safe(root, f'.workflow/archive/{args.id}')
    require(not dest.exists(), 'Archive already exists.')
    commit_paths = sorted(requested | {str(p.relative_to(root)), str(dest.relative_to(root)), str(target.relative_to(root))})
    # Change metadata can be new or tracked; do not stage unrelated .workflow content.
    tracked_change = bool(git(root, 'ls-files', '--', str(p.relative_to(root))).stdout.strip())
    state_before = (p / 'state.json').read_bytes()
    summary_path = safe(p, 'execution-summary.json')
    summary = execution_summary(root, p, s, registry, current) if s['schema_version'] == 2 else None
    summary_before = summary_path.read_bytes().decode('utf-8') if summary is not None and summary_path.exists() else None
    try:
        save(p, s, 'archived')
        if summary is not None:
            write_json(summary_path, summary)
        write(target, (p / 'spec.md').read_text())
        p.rename(dest)
        add_paths = [x for x in commit_paths if x != str(p.relative_to(root)) or tracked_change]
        git(root, 'add', '-A', '--', *add_paths)
        staged = set(os.fsdecode(x) for x in git(root, 'diff', '--cached', '--no-renames', '--name-only', '-z').stdout.split(b'\0') if x)
        require(all(x in requested or x == str(target.relative_to(root)) or x.startswith(str(p.relative_to(root)) + '/') or x.startswith(str(dest.relative_to(root)) + '/') for x in staged), 'Unexpected staged path.')
        git(root, 'commit', '-m', args.message)
    except Exception:
        # Index was empty before this operation. Only unstage the paths owned by this transaction.
        if head(root) == s['base_head']:
            if s['base_head']:
                git(root, 'reset', '-q', 'HEAD', '--', *commit_paths, check=False)
            else:
                git(root, 'rm', '-r', '--cached', '--ignore-unmatch', '--', *commit_paths, check=False)
            if dest.exists():
                dest.rename(p)
            write(p / 'state.json', state_before.decode())
            if summary is not None:
                if summary_before is None:
                    summary_path.unlink(missing_ok=True)
                else:
                    write(summary_path, summary_before)
            if before is None:
                target.unlink(missing_ok=True)
            else:
                write(target, before.decode())
        raise
    commit = head(root)
    require(snapshot(root) == current, 'Commit hook changed the working tree; archive committed but needs review.')
    committed = set(os.fsdecode(x) for x in git(root, 'diff-tree', '--root', '--no-renames', '--no-commit-id', '--name-only', '-r', '-z', commit).stdout.split(b'\0') if x)
    require(committed == staged, 'Commit hook changed commit scope; inspect the created commit before proceeding.')
    require(not git(root, 'diff', 'HEAD', '--', *sorted(requested)).stdout, 'Committed content differs from verified content.')
    return {'phase': 'archived', 'commit': commit, 'archive': str(dest),
            'note': 'No push or deployment performed. Git history identifies the archive commit.'}

@contextlib.contextmanager
def lock(root):
    folder = safe(root, '.workflow')
    folder.mkdir(exist_ok=True)
    for item in folder.rglob('*'):
        require(not item.is_symlink(), f'Symlink inside managed workflow: {item}')
    p = safe(root, '.workflow/.lock')
    try:
        fd = os.open(p, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError as exc:
        raise WorkflowError('Workflow is locked. If a prior process crashed, confirm it stopped before removing .workflow/.lock.') from exc
    try:
        os.write(fd, str(os.getpid()).encode())
        os.close(fd)
        yield
    finally:
        p.unlink(missing_ok=True)

def parser():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', default='.', help='Target project root')
    sub = p.add_subparsers(dest='command', required=True)
    sub.add_parser('bootstrap')
    e = sub.add_parser('explore'); e.add_argument('id'); e.add_argument('--title'); e.add_argument('--target'); e.add_argument('--extension', action='append', default=[])
    for name in ('validate', 'start'):
        sub.add_parser(name).add_argument('id')
    st = sub.add_parser('status'); st.add_argument('id'); st.add_argument('--compact', action='store_true')
    pl = sub.add_parser('plan'); pl.add_argument('id'); pl.add_argument('--file', help='Execution plan JSON file, or - for stdin; omit to read the current plan')
    pl.add_argument('--migrate', action='store_true', help='Explicitly migrate an approved v1 change, invalidating approval')
    e = sub.add_parser('select'); e.add_argument('id'); e.add_argument('--extension', action='append', default=[])
    a = sub.add_parser('approve'); a.add_argument('id'); a.add_argument('--by', required=True); a.add_argument('--ack-user-approval', action='store_true'); a.add_argument('--expected-digest', help='Require the exact contract shown by an interactive client')
    c = sub.add_parser('check'); c.add_argument('id'); c.add_argument('--results', required=True)
    d = sub.add_parser('docs'); d.add_argument('id'); d.add_argument('--summary', required=True); d.add_argument('--paths', nargs='*', default=[])
    a = sub.add_parser('archive'); a.add_argument('id'); a.add_argument('--message', required=True); a.add_argument('--paths', nargs='*', default=[])
    sub.add_parser('snapshot')
    sub.add_parser('extensions')
    return p

def main():
    args = parser().parse_args()
    root = Path(args.root).absolute()
    require(root.is_dir(), 'Project directory does not exist.')
    require(root.resolve() == root, 'Use the physical project path, not a symlink alias.')
    if args.command == 'snapshot':
        files = snapshot(root); result = {'subject_digest': digest(files), 'files': files}
    elif args.command == 'status':
        result = status(root, args)
    elif args.command == 'plan' and not args.file:
        result = plan(root, args)
    elif args.command == 'extensions':
        result = extension_data(root, [k for k,v in config(root)['extensions'].items() if isinstance(v,dict) and v.get('enabled') is True])
    else:
        with lock(root):
            result = globals()[args.command](root, args)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if args.command == 'check' and not result['all_passed']:
        sys.exit(2)

if __name__ == '__main__':
    try:
        main()
    except (WorkflowError, OSError, ValueError, KeyError, TypeError) as exc:
        print(json.dumps({'error': str(exc)}, ensure_ascii=False), file=sys.stderr)
        sys.exit(1)
