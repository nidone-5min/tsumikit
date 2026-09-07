export namespace main {
	
	export class OverlayStatus {
	    running: boolean;
	    port: number;
	    url: string;
	    connections: number;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new OverlayStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.port = source["port"];
	        this.url = source["url"];
	        this.connections = source["connections"];
	        this.error = source["error"];
	    }
	}
	export class YouTubeEvent {
	    author: string;
	    message: string;
	    type: string;
	    publishedAt: string;
	    replayed: boolean;
	    triggerEligible: boolean;
	
	    static createFrom(source: any = {}) {
	        return new YouTubeEvent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.author = source["author"];
	        this.message = source["message"];
	        this.type = source["type"];
	        this.publishedAt = source["publishedAt"];
	        this.replayed = source["replayed"];
	        this.triggerEligible = source["triggerEligible"];
	    }
	}
	export class YouTubeStatus {
	    state: string;
	    authenticated: boolean;
	    connected: boolean;
	    videoId: string;
	    initialMessages: number;
	    realtimeMessages: number;
	    lastEvent?: YouTubeEvent;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new YouTubeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.authenticated = source["authenticated"];
	        this.connected = source["connected"];
	        this.videoId = source["videoId"];
	        this.initialMessages = source["initialMessages"];
	        this.realtimeMessages = source["realtimeMessages"];
	        this.lastEvent = this.convertValues(source["lastEvent"], YouTubeEvent);
	        this.error = source["error"];
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

