#!/usr/bin/env python3
"""Copy explicitly requested v1 extensions into one bootstrapped project.

No global installation, discovery skills, hooks, or change selection is performed.
"""
import argparse
import json
import os
from pathlib import Path
import shutil

import workflow

PACKAGE_ROOT = Path(__file__).resolve().parent
BUNDLED_CATALOG = PACKAGE_ROOT / 'extensions'
CATALOG = BUNDLED_CATALOG if BUNDLED_CATALOG.is_dir() else PACKAGE_ROOT / 'extensions'
PHASES = ('explore', 'apply', 'check', 'docs')


def files(base):
    result = {}
    for path in sorted(base.rglob('*')):
        workflow.require(not path.is_symlink(), f'Symlink not allowed: {path}')
        if path.is_file():
            result[str(path.relative_to(base))] = path.read_bytes()
        else:
            workflow.require(path.is_dir(), f'Unsupported entry: {path}')
    return result


def package(name):
    workflow.slug(name)
    base = workflow.safe(CATALOG, name)
    manifest = workflow.read_json(workflow.safe(base, 'extension.json'))
    workflow.require(manifest.get('schema_version') == 1 and manifest.get('id') == name,
                     f'Invalid manifest: {name}')
    for phase in PHASES:
        rel = manifest.get(phase)
        workflow.require(isinstance(rel, str) and bool(rel), f'Missing {phase}: {name}')
        workflow.require(workflow.safe(base, rel).is_file(), f'Missing reference: {name}/{rel}')
    files(base)
    return base


def install(root, names, dry_run=False):
    workflow.require(root.is_dir() and root.resolve() == root,
                     'Use an existing physical project directory.')
    cfgpath = workflow.safe(root, '.workflow/config.json')
    config = workflow.read_json(cfgpath)
    workflow.require(config.get('schema_version') == 1 and isinstance(config.get('extensions'), dict),
                     'Bootstrap the project first; expected v1 config.')
    copies = []
    entries = []
    for name in dict.fromkeys(names):
        source = package(name)
        relative = f'.workflow/extensions/{name}'
        destination = workflow.safe(root, relative)
        entry = {'enabled': True, 'path': relative}
        existing = config['extensions'].get(name)
        workflow.require(name not in config['extensions'] or existing == entry,
                         f'Existing configuration differs for {name}; review it manually.')
        if destination.exists():
            workflow.require(destination.is_dir() and files(destination) == files(source),
                             f'Existing package differs: {destination}; no overwrite performed.')
        else:
            copies.append((source, destination))
        config['extensions'][name] = entry
        entries.append({'id': name, 'path': relative, 'copy': not destination.exists()})
    result = {'dry_run': dry_run, 'project': str(root), 'available': entries,
              'selected_for_changes': [], 'message': 'Select explicitly during explore.'}
    if dry_run:
        return result
    # Caller holds the same project lock used by the engine. Preflight all packages
    # before copying any; publish config only after all copies have succeeded.
    created = []
    parent = workflow.safe(root, '.workflow/extensions')
    parent_existed = parent.exists()
    try:
        if copies:
            parent.mkdir(exist_ok=True)
        for source, destination in copies:
            destination.mkdir()
            created.append(destination)
            shutil.copytree(source, destination, dirs_exist_ok=True)
        workflow.write_json(cfgpath, config)
    except BaseException:
        for destination in reversed(created):
            if destination.is_dir():
                shutil.rmtree(destination)
        if not parent_existed and parent.exists() and not any(parent.iterdir()):
            parent.rmdir()
        raise
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project', required=True, help='Physical root of a bootstrapped project')
    parser.add_argument('--extension', required=True, action='append', help='Explicit catalogue id; repeat as needed')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    root = Path(args.project).absolute()
    # Validate the path before the engine lock can create a directory.
    workflow.require(root.is_dir() and root.resolve() == root, 'Use an existing physical project directory.')
    workflow.safe(root, '.workflow/config.json')
    workflow.require((root/'.workflow/config.json').is_file(), 'Bootstrap the project first.')
    if args.dry_run:
        result = install(root, args.extension, True)
    else:
        with workflow.lock(root):
            result = install(root, args.extension)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    try:
        main()
    except (workflow.WorkflowError, OSError, ValueError, KeyError, TypeError) as exc:
        print(json.dumps({'error': str(exc)}, ensure_ascii=False), file=os.sys.stderr)
        raise SystemExit(1)
