#!/usr/bin/env python3
"""Self-check of page_records.py against real DBCC PAGE (..., 3) output.

    python3 test_page_records.py

The fixtures are slots cut from pages of SQL Server 2022 CU26 (Linux), on test
data: two heaps, one with corrupted bytes in slot 27, and one clustered index
leaf page whose slot 90 was overwritten without any visible error.
"""
import io
import json
import os
import sys
from contextlib import redirect_stderr, redirect_stdout

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import page_records  # noqa: E402

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'testdata')


def run(name, *args):
    out = io.StringIO()
    with redirect_stdout(out), redirect_stderr(io.StringIO()):
        page_records.main([os.path.join(HERE, name), '--format', 'json', *args])
    return {r['slot']: r for r in json.loads(out.getvalue())}


def test_varchar_max_inline_blobs():
    rows = run('heap-varchar-max.txt',
               '--types', 'Prenom=varchar,Nom=varchar,Adresse=varchar,CP=varchar,Ville=varchar,Tel=varchar,Email=varchar')
    assert sorted(rows) == [0, 34, 46], sorted(rows)
    assert rows[0]['Prenom'] == 'Tamika' and rows[0]['Adresse'] == '261 West Fabien Blvd.', rows[0]
    assert rows[0]['status'] == 'ok', rows[0]
    assert rows[34]['Prenom'] is None, rows[34]            # [NULL] in a varchar(max) column
    # slot 46 carries the deliberately overwritten bytes: same length, zeros
    assert rows[46]['Ville'] == 'Ho00000' and rows[46]['Tel'] == '00000000000', rows[46]


def test_blob_without_types_is_flagged():
    rows = run('heap-varchar-max.txt')
    assert rows[0]['Nom'] == 'Holmes', rows[0]
    assert 'assumed-varchar:Nom' in rows[0]['status'], rows[0]['status']


def test_typed_heap():
    rows = run('heap-typed.txt', '--types', 'Nom=nvarchar,Ville=varchar,Note=varchar,Code=char')
    assert rows[0] == {'slot': 0, 'record_type': 'PRIMARY_RECORD', 'status': 'ok', 'Id': '346',
                       'Nom': 'Nom 346', 'Ville': 'Ville 55', 'Note': 'x' * 26 + ' fin',
                       'Naissance': '1970-12-13', 'Solde': '432.50', 'Actif': '0', 'Code': 'C346'}, rows[0]
    assert rows[4]['Nom'] == 'Nom 350 Éloïse', rows[4]   # nvarchar decoded as UTF-16LE
    assert rows[4]['Ville'] is None, rows[4]
    # the corrupted record: only Solde is detectable, Id and the date look valid
    assert rows[27]['status'] == 'invalid:Solde', rows[27]
    assert rows[27]['Id'] == '808464501' and rows[27]['Naissance'] == '8647-06-28', rows[27]


def test_clustered_leaf_page():
    rows = run('clustered.txt', '--types', 'Libelle=varchar')
    assert sorted(rows) == [0, 90], sorted(rows)            # KeyHashValue headers are not new slots
    assert rows[0]['Libelle'] == 'Libellé 1460' and rows[0]['Montant'] == '1825.00', rows[0]
    # overwritten bytes inside a varchar: decodes cleanly, status ok
    assert rows[90]['Libelle'] == 'LibeZZZZ1550' and rows[90]['status'] == 'ok', rows[90]


def test_sql_output_is_collation_safe():
    out = io.StringIO()
    with redirect_stdout(out), redirect_stderr(io.StringIO()):
        page_records.main([os.path.join(HERE, 'heap-typed.txt'), '--format', 'sql', '--table', '#r',
                           '--types', 'Nom=nvarchar'])
    sql = out.getvalue()
    assert 'COLLATE DATABASE_DEFAULT' in sql
    assert sql.count('INSERT #r') == 3
    assert "N'Nom 350 Éloïse'" in sql


if __name__ == '__main__':
    tests = [v for k, v in sorted(globals().items()) if k.startswith('test_')]
    for t in tests:
        t()
        print('ok', t.__name__)
    print(f'{len(tests)} tests passed')
