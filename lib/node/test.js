// errand's Node, a server asked for: what is said to errand, when it is asked again, what its failures are. The program is a fake one.
import assert from "node:assert/strict";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { serve, http, ServeError } from "./index.js";

const tmp = mkdtempSync( join( tmpdir(), "errand-node-" ) ), log = join( tmp, "asked" ), out = join( tmp, "answer" );
const answer = more => writeFileSync( out, JSON.stringify( { ports: { port: 20001 }, again_in: 100, until: 0, state: "rests", label: "x", config: "abcd",
                                                              address: "127.0.0.1", ...more } ) );
answer();
writeFileSync( log, "" );
writeFileSync( join( tmp, "errand" ), `#!/bin/sh
input=""; case "$*" in *--spec*) input=$(cat) ;; esac
printf '%s\\t%s\\n' "$*" "$input" >> '${log}'
cat '${out}'
` );
chmodSync( join( tmp, "errand" ), 0o755 );
process.env.ERRAND = join( tmp, "errand" );
const asked = () => readFileSync( log, "utf8" ).trim().split( "\n" ).filter( Boolean ).map( l => l.split( "\t" ) );

const s = serve( [ "srv", "--port", "{port}" ], { on: "lmo", vars: { N: 3 }, gpus: 1, busy: http( "/api", { field: "a.b" } ), keep: "2h", label: "x",
                                                hold_end: "2s", listen: "0.0.0.0" } );
assert.deepEqual( asked(), [], "nothing is asked before its url is" );
const [ a, b ] = await Promise.all( [ s.url( "/a" ), s.url() ] );
assert.equal( a, "http://127.0.0.1:20001/a" );
assert.equal( b, "http://127.0.0.1:20001" );
assert.equal( asked().length, 1, "asked once, for both" );
assert.equal( asked()[ 0 ][ 0 ], "serve --spec - --json" );
assert.deepEqual( JSON.parse( asked()[ 0 ][ 1 ] ), { command: [ "srv", "--port", "{port}" ], vars: { N: "3" }, on: "lmo", gpus: 1,
  busy: { http: "/api", field: "a.b" }, keep: "2h", label: "x", listen: "0.0.0.0", hold_end: "2s" } );
s._again = Date.now() - 1;
await s.url();
assert.equal( asked().length, 2, "asked again at half of its lease" );
await s.stop();
await s.release( { now: true } );
assert.deepEqual( asked().slice( 2 ).map( x => x[ 0 ] ), [ "serve stop x~abcd", "serve release x~abcd --now" ] );

const several = serve( [ "t" ], { ports: { http: 8000, grpc: [ null, 18001 ], m: null } } );
answer( { ports: { http: 8000, grpc: 20002, m: 20003 } } );
assert.equal( await several.port(), 8000 );
assert.equal( await several.url( "", "grpc" ), "http://127.0.0.1:20002" );
assert.deepEqual( JSON.parse( asked().at( -1 )[ 1 ] ).ports, [ [ "http", "8000" ], [ "grpc", ":18001" ], [ "m", null ] ] );
await assert.rejects( several.port( "no" ), RangeError );

// a connection refused is asked for again; one that failed says why
answer();
const t = serve( [ "x" ] ), seen = [];
const once = err => async url => {
  seen.push( url );
  if ( seen.length === 1 ) { answer( { ports: { port: 20009 } } ); throw err; }
  return "done";
};
const refused = Object.assign( new TypeError( "fetch failed" ), { cause: Object.assign( new Error( "refused" ), { code: "ECONNREFUSED" } ) } );
assert.equal( await t.call( once( refused ), "/a" ), "done" );
assert.deepEqual( seen, [ "http://127.0.0.1:20001/a", "http://127.0.0.1:20009/a" ] );
answer( { state: "failed", why: "exit status 3" } );
await assert.rejects( t.call( async () => { throw refused; } ), err => err instanceof ServeError && /exit status 3/.test( err.message ) && err.cause === refused );
answer();
const own = new Error( "its own" );
await assert.rejects( t.call( async () => { throw own; } ), err => err === own );
writeFileSync( out, JSON.stringify( { error: "no such program" } ) );
await assert.rejects( serve( [ "x" ] ).url(), err => err instanceof ServeError && /no such program/.test( err.message ) );

assert.throws( () => serve( "x y" ), TypeError );
assert.throws( () => serve( [ "x" ], { gpu: 1 } ), /gpu is not said/ );
console.log( "ok" );
