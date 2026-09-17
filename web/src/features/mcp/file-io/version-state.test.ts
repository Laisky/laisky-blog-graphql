import * as assert from 'node:assert/strict';
import { describe, it } from 'vitest';
import { canonicalFilePath, expectedVersion, fileIOErrorMessage, fileSnapshot, FileIORequestError, ifMatch, renameCondition, RequestEpoch, requireFileVersion, writeCondition } from './version-state';

const identity = '76c3e40a3c4b4f5d9c2617d1b8094593';
const token = `${identity}:9007199254740993`;
const snapshot = fileSnapshot('/a', { content: 'key=漢0', content_encoding: 'utf-8', version: token });

describe('FileIO browser snapshot protocol', () => {
  it('keeps Unicode bytes and a large counter together without numeric conversion', () => {
    assert.equal(snapshot.content, 'key=漢0');
    assert.equal(snapshot.version, token);
    assert.deepEqual(ifMatch(snapshot, '/a'), { 'If-Match': `"${token}"` });
  });
  it('canonicalizes only the request path, not the opaque version', () => {
    assert.equal(canonicalFilePath('a'), '/a');
    assert.equal(expectedVersion(snapshot, 'a'), token);
    assert.equal(canonicalFilePath(''), '');
  });
  it('fails closed on absent, numeric, malformed, noncanonical or overflowing versions', () => {
    for (const value of [undefined, null, 1, '', 'broken', `${identity}:01`, `${identity}:0`, `${identity}:9223372036854775808`]) {
      assert.throws(() => requireFileVersion(value));
    }
    assert.equal(requireFileVersion(`${identity}:9223372036854775807`), `${identity}:9223372036854775807`);
  });
  it('never turns a missing or wrong-path snapshot into a blind edit', () => {
    assert.throws(() => writeCondition(null, '/a', false));
    assert.throws(() => expectedVersion(snapshot, '/b'));
    assert.throws(() => ifMatch(null, '/a'));
  });
  it('emits exactly one write condition', () => {
    assert.deepEqual(writeCondition(snapshot, '/a', false), { expected_version: token });
    assert.deepEqual(writeCondition(snapshot, '/new', true), { create_only: true });
  });
  it('keeps independently read versions immutable when another observation arrives', () => {
    const newer = fileSnapshot('/a', { ...snapshot, content: 'later', version: `${identity}:9007199254740994` });
    assert.equal(expectedVersion(snapshot, '/a'), token);
    assert.equal(expectedVersion(newer, '/a'), `${identity}:9007199254740994`);
    assert.equal(snapshot.content, 'key=漢0');
  });
  it('rejects an encoded history payload as a live edit snapshot', () => {
    assert.throws(() => fileSnapshot('/a', { content: '/w==', content_encoding: 'base64', version: token }));
    assert.throws(() => fileSnapshot('/a', { content: 'text', content_encoding: 'utf-8', version: '' }));
  });
  it('protects both names when overwriting, and requires absence otherwise', () => {
    const destination = fileSnapshot('/b', { ...snapshot, version: `${identity}:9` });
    assert.deepEqual(renameCondition(snapshot, '/a', '/b', false, null), {
      expected_version: token, overwrite: false, destination_must_not_exist: true,
    });
    assert.deepEqual(renameCondition(snapshot, '/a', '/b', true, destination), {
      expected_version: token, overwrite: true, expected_destination_version: `${identity}:9`,
    });
    assert.throws(() => renameCondition(snapshot, '/a', '/b', true, null));
    assert.throws(() => renameCondition(snapshot, '/a', '/b', true, snapshot));
    assert.throws(() => renameCondition(snapshot, '/a', 'a', false, null));
  });
  it('invalidates an older response even if the path is selected again later', () => {
    const lane = new RequestEpoch();
    const oldA = lane.next();
    const b = lane.next();
    const newA = lane.next();
    assert.equal(lane.current(oldA), false);
    assert.equal(lane.current(b), false);
    assert.equal(lane.current(newA), true);
  });
  it('invalidates responses on close, scope change and unmount', () => {
    const lane = new RequestEpoch();
    const request = lane.next();
    lane.invalidate();
    assert.equal(lane.current(request), false);
  });
  it('has independent ordering for separate request lanes', () => {
    const a = new RequestEpoch();
    const b = new RequestEpoch();
    const first = a.next();
    b.next();
    assert.equal(a.current(first), true);
  });
  it('explains conflict recovery for both MCP and HTTP without an automatic retry', () => {
    for (const err of [new FileIORequestError('stale', 'VERSION_CONFLICT'), new FileIORequestError('stale', undefined, 412)]) {
      assert.match(fileIOErrorMessage(err), /Your draft is kept/);
      assert.match(fileIOErrorMessage(err), /recompute/);
      assert.match(fileIOErrorMessage(err), /Nothing was retried automatically/);
    }
  });
  it('distinguishes missing preconditions from conflicts and network errors', () => {
    assert.match(fileIOErrorMessage(new FileIORequestError('required', 'PRECONDITION_REQUIRED')), /version is required/);
    assert.match(fileIOErrorMessage(new FileIORequestError('required', undefined, 428)), /version is required/);
    assert.equal(fileIOErrorMessage(new Error('network')), 'network');
  });
});
