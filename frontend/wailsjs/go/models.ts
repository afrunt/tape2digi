export namespace audio {
	
	export class ChannelLevel {
	    peakDb: number;
	    rmsDb: number;
	    clip: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ChannelLevel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peakDb = source["peakDb"];
	        this.rmsDb = source["rmsDb"];
	        this.clip = source["clip"];
	    }
	}
	export class Device {
	    id: string;
	    name: string;
	    isDefault: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Device(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.isDefault = source["isDefault"];
	    }
	}

}

export namespace exporter {
	
	export class FormatInfo {
	    id: string;
	    available: boolean;
	    reason: string;
	    bitrates?: number[];
	    rates?: number[];
	    rate?: number;
	
	    static createFrom(source: any = {}) {
	        return new FormatInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.available = source["available"];
	        this.reason = source["reason"];
	        this.bitrates = source["bitrates"];
	        this.rates = source["rates"];
	        this.rate = source["rate"];
	    }
	}
	export class Options {
	    format: string;
	    bitrate: number;
	    sampleRate: number;
	    embedCover: boolean;
	    coverMaxMB: number;
	    dir: string;
	    exportOriginal: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Options(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.format = source["format"];
	        this.bitrate = source["bitrate"];
	        this.sampleRate = source["sampleRate"];
	        this.embedCover = source["embedCover"];
	        this.coverMaxMB = source["coverMaxMB"];
	        this.dir = source["dir"];
	        this.exportOriginal = source["exportOriginal"];
	    }
	}
	export class Plan {
	    tracks: number;
	    durationMs: number;
	    hasCover: boolean;
	    formats: FormatInfo[];
	    originals: string[];
	
	    static createFrom(source: any = {}) {
	        return new Plan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tracks = source["tracks"];
	        this.durationMs = source["durationMs"];
	        this.hasCover = source["hasCover"];
	        this.formats = this.convertValues(source["formats"], FormatInfo);
	        this.originals = source["originals"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ExportResult {
	    dir: string;
	    files: string[];
	
	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.files = source["files"];
	    }
	}
	export class MarkerChoice {
	    start: boolean;
	    end: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MarkerChoice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start = source["start"];
	        this.end = source["end"];
	    }
	}
	export class MarkerSpan {
	    min: number;
	    max: number;
	
	    static createFrom(source: any = {}) {
	        return new MarkerSpan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.min = source["min"];
	        this.max = source["max"];
	    }
	}
	export class NoisePlan {
	    available: boolean;
	    noiseDb: number;
	
	    static createFrom(source: any = {}) {
	        return new NoisePlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.noiseDb = source["noiseDb"];
	    }
	}
	export class SideGain {
	    available: boolean;
	    db: number;
	
	    static createFrom(source: any = {}) {
	        return new SideGain(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.db = source["db"];
	    }
	}
	export class SidePlayback {
	    side: string;
	    active: boolean;
	    playing: boolean;
	    positionMs: number;
	    durationMs: number;
	    levels: audio.ChannelLevel[];
	
	    static createFrom(source: any = {}) {
	        return new SidePlayback(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.side = source["side"];
	        this.active = source["active"];
	        this.playing = source["playing"];
	        this.positionMs = source["positionMs"];
	        this.durationMs = source["durationMs"];
	        this.levels = this.convertValues(source["levels"], audio.ChannelLevel);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SplitPlan {
	    available: boolean;
	    tracks: number;
	
	    static createFrom(source: any = {}) {
	        return new SplitPlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.tracks = source["tracks"];
	    }
	}
	export class Waveform {
	    channels: number;
	    sampleRate: number;
	    durationMs: number;
	    min: number[][];
	    max: number[][];
	    gainAvailable: boolean;
	    gainDb: number;
	
	    static createFrom(source: any = {}) {
	        return new Waveform(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channels = source["channels"];
	        this.sampleRate = source["sampleRate"];
	        this.durationMs = source["durationMs"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.gainAvailable = source["gainAvailable"];
	        this.gainDb = source["gainDb"];
	    }
	}

}

export namespace project {
	
	export class History {
	    canUndo: boolean;
	    canRedo: boolean;
	
	    static createFrom(source: any = {}) {
	        return new History(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.canUndo = source["canUndo"];
	        this.canRedo = source["canRedo"];
	    }
	}
	export class Leftover {
	    side: string;
	    bytes: number;
	    durationMs: number;
	
	    static createFrom(source: any = {}) {
	        return new Leftover(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.side = source["side"];
	        this.bytes = source["bytes"];
	        this.durationMs = source["durationMs"];
	    }
	}
	export class Marker {
	    kind: string;
	    positionMs: number;
	    title?: string;
	    artist?: string;
	    albumArtist?: string;
	
	    static createFrom(source: any = {}) {
	        return new Marker(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.positionMs = source["positionMs"];
	        this.title = source["title"];
	        this.artist = source["artist"];
	        this.albumArtist = source["albumArtist"];
	    }
	}
	export class Side {
	    file: string;
	    origin: string;
	    // Go type: time
	    recordedAt: any;
	    durationMs: number;
	    sampleRate: number;
	    channels: number;
	    bitDepth: number;
	    device?: string;
	    sourceName?: string;
	    missing?: boolean;
	    staged?: boolean;
	    markers?: Marker[];
	
	    static createFrom(source: any = {}) {
	        return new Side(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file = source["file"];
	        this.origin = source["origin"];
	        this.recordedAt = this.convertValues(source["recordedAt"], null);
	        this.durationMs = source["durationMs"];
	        this.sampleRate = source["sampleRate"];
	        this.channels = source["channels"];
	        this.bitDepth = source["bitDepth"];
	        this.device = source["device"];
	        this.sourceName = source["sourceName"];
	        this.missing = source["missing"];
	        this.staged = source["staged"];
	        this.markers = this.convertValues(source["markers"], Marker);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Info {
	    dir: string;
	    name: string;
	    cover: string;
	    coverMissing: boolean;
	    coverDraft: boolean;
	    artist: string;
	    compilation: boolean;
	    year: string;
	    album: string;
	    genre: string;
	    dirty: boolean;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	    sides: Record<string, Side>;
	    leftovers: Leftover[];
	
	    static createFrom(source: any = {}) {
	        return new Info(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.name = source["name"];
	        this.cover = source["cover"];
	        this.coverMissing = source["coverMissing"];
	        this.coverDraft = source["coverDraft"];
	        this.artist = source["artist"];
	        this.compilation = source["compilation"];
	        this.year = source["year"];
	        this.album = source["album"];
	        this.genre = source["genre"];
	        this.dirty = source["dirty"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.sides = this.convertValues(source["sides"], Side, true);
	        this.leftovers = this.convertValues(source["leftovers"], Leftover);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LastHistory {
	    canUndo: boolean;
	    canRedo: boolean;
	    undoSide: string;
	    redoSide: string;
	
	    static createFrom(source: any = {}) {
	        return new LastHistory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.canUndo = source["canUndo"];
	        this.canRedo = source["canRedo"];
	        this.undoSide = source["undoSide"];
	        this.redoSide = source["redoSide"];
	    }
	}
	
	
	export class RecentEntry {
	    path: string;
	    name: string;
	    // Go type: time
	    lastOpened: any;
	    available: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RecentEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.lastOpened = this.convertValues(source["lastOpened"], null);
	        this.available = source["available"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace recorder {
	
	export class Status {
	    state: string;
	    monitor: boolean;
	    elapsedMs: number;
	    sizeBytes: number;
	    file: string;
	    overruns: number;
	    channels: number;
	    side: string;
	    importSide: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.monitor = source["monitor"];
	        this.elapsedMs = source["elapsedMs"];
	        this.sizeBytes = source["sizeBytes"];
	        this.file = source["file"];
	        this.overruns = source["overruns"];
	        this.channels = source["channels"];
	        this.side = source["side"];
	        this.importSide = source["importSide"];
	    }
	}

}

export namespace settings {
	
	export class ExportPrefs {
	    format: string;
	    bitrate: number;
	    wavRate: number;
	    coverMaxMB: number;
	    embedCover: boolean;
	    exportOriginal: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ExportPrefs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.format = source["format"];
	        this.bitrate = source["bitrate"];
	        this.wavRate = source["wavRate"];
	        this.coverMaxMB = source["coverMaxMB"];
	        this.embedCover = source["embedCover"];
	        this.exportOriginal = source["exportOriginal"];
	    }
	}
	export class Rect {
	    x: number;
	    y: number;
	    w: number;
	    h: number;
	
	    static createFrom(source: any = {}) {
	        return new Rect(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.y = source["y"];
	        this.w = source["w"];
	        this.h = source["h"];
	    }
	}
	export class Window {
	    x: number;
	    y: number;
	    w: number;
	    h: number;
	    maximised: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Window(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.y = source["y"];
	        this.w = source["w"];
	        this.h = source["h"];
	        this.maximised = source["maximised"];
	    }
	}
	export class Settings {
	    deviceName: string;
	    sampleRate: number;
	    channels: number;
	    bitDepth: number;
	    monitorDeviceName: string;
	    language: string;
	    theme: string;
	    fontScale: number;
	    window?: Window;
	    editorWindow?: Rect;
	    export?: ExportPrefs;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceName = source["deviceName"];
	        this.sampleRate = source["sampleRate"];
	        this.channels = source["channels"];
	        this.bitDepth = source["bitDepth"];
	        this.monitorDeviceName = source["monitorDeviceName"];
	        this.language = source["language"];
	        this.theme = source["theme"];
	        this.fontScale = source["fontScale"];
	        this.window = this.convertValues(source["window"], Window);
	        this.editorWindow = this.convertValues(source["editorWindow"], Rect);
	        this.export = this.convertValues(source["export"], ExportPrefs);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

