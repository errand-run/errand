export class ServeError extends Error {}

export interface Probe { http: string; field?: string; port?: string }
export function http( path: string, options?: { field?: string; port?: string } ): Probe;

export type Layer = { type: string; [ key: string ]: unknown };
export type PortSpec = string | number | null | [ number | null, number | null ];

export interface ServeOptions {
  on?: string | Layer[];
  vars?: Record<string, string | number>;
  cwd?: string;
  gpus?: number | string; gpu_ram?: string; ram?: string; cpus?: number | string;
  exclusive?: boolean;
  ports?: PortSpec | Record<string, PortSpec>;
  others?: boolean;
  busy?: string | Probe; ready?: string | Probe;
  idle?: string | number; grace?: string | number; keep?: string | number;
  label?: string;
  version?: string | number; version_of?: string[];
  listen?: string; always?: boolean; hold_end?: string | number;
}

export interface Said {
  id: string; label: string; config: string; url: string; urls: Record<string, string>; ports: Record<string, number>;
  until: number; again_in: number; state: string; why?: string; host: string; on?: string; address: string;
}

export class Server {
  readonly request: Record<string, unknown>;
  ask(): Promise<Said>;
  port( name?: string ): Promise<number>;
  url( path?: string, port?: string ): Promise<string>;
  call<T>( fn: ( url: string ) => Promise<T> | T, path?: string, port?: string ): Promise<T>;
  state(): Promise<string>;
  wait(): Promise<Server>;
  stop(): Promise<void>;
  release( options?: { now?: boolean } ): Promise<void>;
}

export function serve( command: string[], options?: ServeOptions ): Server;
