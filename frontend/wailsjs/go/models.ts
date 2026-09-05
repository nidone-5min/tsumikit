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

}

