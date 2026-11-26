// A server, asked for in full ( ADR 0040 ): errand gives it a port of this machine, starts it at the first connection, and stops it
// when nobody uses it. The same as errand's Python ( `errand.serve` ), for Node.
//
//     import { serve, http } from "errand-lib";
//     const ollama = serve( [ "ollama", "serve" ], { on: "lmo", vars: { OLLAMA_HOST: "127.0.0.1:{port}" }, gpus: 1, gpu_ram: "10G",
//                           busy: http( "/api/ps", { field: "models" } ), idle: "2m", keep: "1d", label: "ollama" } );
//     const r = await ollama.call( url => fetch( url, { method: "POST", body } ), "/api/chat" );
//
// Nothing is started by `serve`, nor asked before the first `url`. The url is asked of memory, and of errand again at half of the
// lease: to be asked before each request rather than kept. All of it goes through `errand serve --json`.

import { spawn } from "node:child_process";

/** A server that errand refuses, or that does not start: why. */
export class ServeError extends Error {
  constructor( message, options ) {
    super( message, options );
    this.name = "ServeError";
  }
}

/** A probe, for `busy` and `ready`: yes when `field` ( `a.b` goes inside ) of the JSON the server answers at `path` is there, and
 *  neither false, 0 nor empty. `port`: which of its ports ( default: the first ). */
export function http( path, { field, port } = {} ) {
  return { http: path, ...( field ? { field } : {} ), ...( port ? { port } : {} ) };
}

const names = [ "on", "vars", "cwd", "gpus", "gpu_ram", "ram", "cpus", "exclusive", "ports", "others", "busy", "ready", "idle", "grace",
                "keep", "label", "version", "version_of", "listen", "always", "hold_end" ];

/** A server, by all that makes it ( the same words as errand's Python ). Two requests that say the same are one server.
 *
 *  `command`: a list, `{port}` in it and in `vars` being the server's side of its port ( `{port:NAME}` of several ). `on`: the
 *  machine it runs on, by its ssh name, or layers as data ( `[ { type: "Ssh", host: "lmo" }, { type: "Micromamba", name: "jax" } ]` ).
 *  Of its port only: `others`, `listen`, `always`, `hold_end`. Not of what it is: `idle`, `grace`, `keep`, `label`. */
export function serve( command, options = {} ) {
  if ( typeof command === "string" )
    throw new TypeError( "serve: the command as a list of words ( a string would have to be split as a shell does )" );
  for ( const k of Object.keys( options ) )
    if ( !names.includes( k ) )
      throw new TypeError( `serve: ${k} is not said of a server ( ${names.join( ", " )} )` );
  const o = options, said = { command: command.map( String ) };
  if ( o.vars ) said.vars = Object.fromEntries( Object.entries( o.vars ).map( ( [ k, v ] ) => [ k, String( v ) ] ) );
  if ( o.ports !== undefined ) said.ports = ports( o.ports );
  if ( o.version_of ) said.version_of = o.version_of.map( String );
  for ( const k of names )
    if ( !( k in said ) && o[ k ] !== undefined && o[ k ] !== null && o[ k ] !== false )
      said[ k ] = o[ k ];
  return new Server( said );
}

function ports( p ) {
  const all = p !== null && typeof p === "object" && !Array.isArray( p ) ? p : { port: p };
  return Object.entries( all ).map( ( [ name, spec ] ) => [ name, spec === null || spec === undefined ? null :
    Array.isArray( spec ) ? spec.map( s => s === null || s === undefined ? "" : String( s ) ).join( ":" ) : String( spec ) ] );
}

function around( err, seen = new Set() ) {
  const out = [];
  if ( !err || typeof err !== "object" || seen.has( err ) ) return out;
  seen.add( err );
  out.push( err );
  for ( const e of [ err.cause, ...( Array.isArray( err.errors ) ? err.errors : [] ) ] )
    out.push( ...around( e, seen ) );
  return out;
}

const refused = err => around( err ).some( e => e.code === "ECONNREFUSED" );
const cut = err => around( err ).some( e => [ "ECONNREFUSED", "ECONNRESET", "EPIPE", "UND_ERR_SOCKET", "UND_ERR_CLOSED" ].includes( e.code ) );

/** The program: `$ERRAND`, else `errand` as it is found. */
const program = () => process.env.ERRAND || "errand";

function run( words, input ) {
  return new Promise( ( resolve, reject ) => {
    const child = spawn( program(), words, { stdio: [ "pipe", "pipe", "inherit" ] } );
    let out = "";
    child.stdout.setEncoding( "utf8" );
    child.stdout.on( "data", d => out += d );
    child.on( "error", err => reject( new ServeError( `errand: ${err.message}`, { cause: err } ) ) );
    child.on( "close", code => resolve( { out, code } ) );
    child.stdin.end( input ?? "" );
  } );
}

/** A server errand keeps: where it is, at each call ( `serve` ). */
export class Server {
  constructor( request ) {
    this.request = request;
    this._said = null;
    this._again = 0;
    this._asking = null;
  }

  async _run( ...options ) {
    const { out, code } = await run( [ "serve", "--spec", "-", "--json", ...options ], JSON.stringify( this.request ) );
    let said;
    try {
      said = JSON.parse( out );
    } catch {
      throw new ServeError( `errand did not answer ( exit ${code} )` );
    }
    if ( "error" in said ) throw new ServeError( said.error );
    return said;
  }

  _keep( said ) {
    this._said = said;
    this._again = Date.now() + Math.max( said.again_in, 1 ) * 1000;
    return said;
  }

  /** errand asked now, whatever is remembered: its answer ( `url`, `urls`, `ports`, `until`, `state`, `why`, … ) */
  async ask( ...options ) {
    if ( !this._asking ) // ( one question at a time: those who ask meanwhile have its answer )
      this._asking = this._run( ...options ).then( s => this._keep( s ) ).finally( () => { this._asking = null; } );
    return this._asking;
  }

  async _known() {
    if ( this._said === null || Date.now() >= this._again ) return this.ask();
    return this._said;
  }

  /** its port, on this machine ( the one of that name, when it has several ) */
  async port( name ) {
    const ports = ( await this._known() ).ports;
    if ( name === undefined ) name = this.request.ports?.[ 0 ]?.[ 0 ] ?? Object.keys( ports )[ 0 ];
    if ( !( name in ports ) ) throw new RangeError( `no port ${JSON.stringify( name )}: ${Object.keys( ports ).join( ", " )}` );
    return Number( ports[ name ] );
  }

  /** where it is reached, to be asked before each request */
  async url( path = "", port ) {
    const n = await this.port( port );
    return `http://${this._said.address ?? "127.0.0.1"}:${n}${path}`;
  }

  /** `fn( url )`, once more when its connection is refused: errand is asked again meanwhile ( a machine that was restarted ). A
   *  server that does not start throws `ServeError`, which says why. */
  async call( fn, path = "", port ) {
    for ( const again of [ true, false ] ) {
      try {
        return await fn( await this.url( path, port ) );
      } catch ( err ) {
        if ( !cut( err ) ) throw err;
        const said = await this.ask();
        if ( said.state === "failed" || said.state === "away" )
          throw new ServeError( `${said.label}: ${said.why || said.state}`, { cause: err } );
        if ( !( again && refused( err ) ) ) throw err;
      }
    }
  }

  /** how it is now: rests, waiting, starting, warm, in use, busy, failed, away */
  async state() { return ( await this.ask() ).state; }

  /** started now, if it rests; resolves once it answers */
  async wait() {
    this._keep( await this._run( "--wait" ) );
    return this;
  }

  async _named( verb, ...options ) {
    const said = await this._known();
    const { code } = await run( [ "serve", verb, `${said.label}~${said.config}`, ...options ] );
    if ( code !== 0 ) throw new ServeError( `errand serve ${verb}: exit ${code}` );
  }

  /** it stops, for everybody; its port stays, and the next connection starts it again */
  async stop() { await this._named( "stop" ); }

  /** its port is given back; it stops when nobody uses it ( `now`: at once, for everybody ) */
  async release( { now = false } = {} ) {
    await this._named( "release", ...( now ? [ "--now" ] : [] ) );
    this._said = null;
  }
}
