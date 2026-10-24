#!/usr/bin/env python3
"""Extract the records of a data page from the text output of DBCC PAGE (..., 3).

DBCC PAGE decodes the records of a page even when its checksum is wrong, which
is what makes rows recoverable before REPAIR_ALLOW_DATA_LOSS deallocates the
page. This script turns that text into rows.

    page_records.py page.txt                        # TSV on stdout
    page_records.py page.txt --format sql           # temp table + INSERTs
    page_records.py page.txt --types Nom=nvarchar,Note=varchar --codepage cp1252

Each output row carries:
    slot         slot number in the page
    record_type  PRIMARY_RECORD, GHOST_DATA_RECORD, FORWARDING_STUB, ...
    status       ok, or a ';'-separated list of problems (see below)
    one column per table column, in column-number order; NULL is \\N in TSV

String columns are decoded from the record bytes (offset and length given by
DBCC PAGE), not from the displayed text, so trailing spaces survive. Declare
the type of each string column with --types: varchar/char are decoded with
--codepage, nvarchar/nchar as UTF-16LE. Without --types, a column shown as
plain text keeps the displayed text (right-trimmed, so trailing spaces are
lost) and a [BLOB Inline Data] column is decoded as varchar, which is wrong
for nvarchar(max): the status then says "assumed-varchar:<col>".

Status values:
    invalid:<col>    DBCC PAGE printed INVALID COLUMN VALUE
    undecodable:<col>  the bytes do not decode in the declared type
    offrow:<col>     the value lives on another page (LOB root, row overflow,
                     text pointer): this page alone cannot recover it
    short:<col>      the column runs past the bytes of the record dump
    record-type      the record is not a PRIMARY_RECORD (ghost, stub, ...)

Fixed-width values (int, date, decimal, bit, ...) are taken as displayed. A
corrupted int still displays as an int: only the comparison with a sibling
copy of the data tells a wrong value from a right one.

Standard library only.
"""
import argparse
import json
import re
import sys

SLOT_RE = re.compile(r'^Slot (\d+) Offset 0x[0-9a-fA-F]+ Length (\d+)')
RTYPE_RE = re.compile(r'^Record Type = (\S+)')
DUMP_RE = re.compile(r'^([0-9A-F]{16}):\s+([0-9a-f]{2,8}(?: [0-9a-f]{2,8}){0,4})')
COLHDR_RE = re.compile(r'^(?:(\w+) = \[BLOB Inline Data\] )?Slot (\d+) Column (\d+) Offset 0x([0-9a-fA-F]+) Length (\d+)')
VALUE_RE = re.compile(r'^(\w+) = (.*)$')

STRING_TYPES = {'varchar': 'cp', 'char': 'cp', 'nvarchar': 'utf16', 'nchar': 'utf16'}


def parse_types(spec):
    types = {}
    for item in filter(None, (spec or '').split(',')):
        name, _, typ = item.partition('=')
        typ = typ.strip().lower()
        if typ not in STRING_TYPES and typ not in ('binary', 'varbinary', 'other'):
            sys.exit(f'unknown type {typ!r} for {name!r}: use varchar, char, nvarchar, nchar, binary, varbinary or other')
        types[name.strip()] = typ
    return types


def parse(lines):
    """Yield one dict per record: slot, length, record_type, bytes, columns."""
    rec = None
    state = None          # 'record-dump' while reading the record's memory dump
    col = None            # (colno, name, offset, length) awaiting its value line
    for line in lines:
        line = line.rstrip('\n')
        m = SLOT_RE.match(line)
        if m and rec and int(m[1]) == rec['slot']:
            # index pages repeat "Slot N Offset 0x0 Length 0" before KeyHashValue
            state, col = None, None
            continue
        if m:
            if rec:
                yield rec
            rec = {'slot': int(m[1]), 'length': int(m[2]), 'record_type': None,
                   'hex': '', 'columns': {}}
            state, col = None, None
            continue
        if rec is None:
            continue
        m = RTYPE_RE.match(line)
        if m:
            rec['record_type'] = m[1]
            continue
        if line.startswith('Memory Dump @'):
            state = 'record-dump'
            continue
        m = COLHDR_RE.match(line)
        if m:
            state = None
            name, colno, offset, length = m[1], int(m[3]), int(m[4], 16), int(m[5])
            col = (colno, name, offset, length)
            if name:  # [BLOB Inline Data]: the header line is the value line
                rec['columns'][colno] = {'name': name, 'offset': offset, 'length': length,
                                         'display': None, 'blob': True}
                col = None
            continue
        m = DUMP_RE.match(line)
        if m:
            if state == 'record-dump':
                rec['hex'] += m[2].replace(' ', '')
            continue
        m = VALUE_RE.match(line)
        if m and col and m[1] != 'Record':
            colno, _, offset, length = col
            rec['columns'][colno] = {'name': m[1], 'offset': offset, 'length': length,
                                     'display': m[2].rstrip(), 'blob': False}
            col = None
            continue
    if rec:
        yield rec


def decode(rec, types, codepage):
    data = bytes.fromhex(rec['hex'])
    problems = []
    if rec['record_type'] != 'PRIMARY_RECORD':
        problems.append('record-type')
    values = {}
    for colno in sorted(rec['columns']):
        c = rec['columns'][colno]
        name, disp = c['name'], c['display']
        typ = types.get(name)
        if disp == '[NULL]':
            values[name] = None
            continue
        if disp is not None and disp.startswith('[') and not c['blob']:
            problems.append(f'offrow:{name}')
            values[name] = None
            continue
        if disp == 'INVALID COLUMN VALUE':
            problems.append(f'invalid:{name}')
            values[name] = None
            continue
        raw = data[c['offset']:c['offset'] + c['length']]
        if len(raw) < c['length']:
            problems.append(f'short:{name}')
        if typ in ('binary', 'varbinary'):
            values[name] = '0x' + raw.hex().upper()
            continue
        kind = STRING_TYPES.get(typ)
        if kind is None and c['blob']:
            kind = 'cp'
            problems.append(f'assumed-varchar:{name}')
        if kind is None:
            values[name] = disp
            continue
        try:
            values[name] = raw.decode('utf-16-le' if kind == 'utf16' else codepage, errors='strict')
        except UnicodeDecodeError:
            problems.append(f'undecodable:{name}')
            values[name] = raw.decode('utf-16-le' if kind == 'utf16' else codepage, errors='replace')
    return values, problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('dump', help='text output of DBCC PAGE (db, file, page, 3), "-" for stdin')
    ap.add_argument('--format', choices=('tsv', 'json', 'sql'), default='tsv')
    ap.add_argument('--types', help='Col=type,... for string and binary columns')
    ap.add_argument('--codepage', default='cp1252', help='code page of varchar/char columns (default cp1252)')
    ap.add_argument('--table', default='#recovered', help='staging table name for --format sql')
    args = ap.parse_args(argv)

    types = parse_types(args.types)
    src = sys.stdin if args.dump == '-' else open(args.dump, encoding='utf-8', errors='replace')
    with src:
        records = list(parse(src))
    if not records:
        sys.exit('no record found: is this the output of DBCC PAGE (..., 3) with trace flag 3604 on?')

    names = []
    for r in records:
        for colno in sorted(r['columns']):
            n = r['columns'][colno]['name']
            if n not in names:
                names.append(n)
    rows = []
    for r in records:
        values, problems = decode(r, types, args.codepage)
        rows.append({'slot': r['slot'], 'record_type': r['record_type'],
                     'status': ';'.join(problems) or 'ok',
                     **{n: values.get(n) for n in names}})

    out = sys.stdout
    if args.format == 'json':
        json.dump(rows, out, ensure_ascii=False, indent=1)
        out.write('\n')
    elif args.format == 'tsv':
        def cell(v):
            if v is None:
                return '\\N'
            return str(v).replace('\\', '\\\\').replace('\t', '\\t').replace('\n', '\\n').replace('\r', '\\r')
        out.write('\t'.join(['slot', 'record_type', 'status'] + names) + '\n')
        for row in rows:
            out.write('\t'.join(cell(row[k]) for k in ['slot', 'record_type', 'status'] + names) + '\n')
    else:
        def lit(v):
            return 'NULL' if v is None else "N'" + str(v).replace("'", "''") + "'"
        cols = ',\n    '.join(f'[{n}] nvarchar(max) COLLATE DATABASE_DEFAULT NULL' for n in names)
        out.write('SET NOCOUNT ON;\n')
        out.write(f"CREATE TABLE {args.table} (\n    Slot int NOT NULL,\n    RecordType varchar(40) COLLATE DATABASE_DEFAULT NULL,\n"
                  f"    Status nvarchar(400) COLLATE DATABASE_DEFAULT NOT NULL,\n    {cols});\n")
        collist = ', '.join(['Slot', 'RecordType', 'Status'] + [f'[{n}]' for n in names])
        for row in rows:
            vals = [str(row['slot']), lit(row['record_type']), lit(row['status'])] + [lit(row[n]) for n in names]
            out.write(f"INSERT {args.table} ({collist}) VALUES ({', '.join(vals)});\n")
    bad = sum(1 for r in rows if r['status'] != 'ok')
    print(f'{len(rows)} records, {bad} with a status other than ok', file=sys.stderr)


if __name__ == '__main__':
    main()
