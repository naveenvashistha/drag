export namespace main {
	
	export class DirectoryState {
	    IsValid: boolean;
	    IsWatched: boolean;
	    IsPublic: boolean;
	    IsIgnored: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DirectoryState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.IsValid = source["IsValid"];
	        this.IsWatched = source["IsWatched"];
	        this.IsPublic = source["IsPublic"];
	        this.IsIgnored = source["IsIgnored"];
	    }
	}
	export class FileDisplayInfo {
	    fileName: string;
	    path: string;
	    size: number;
	    status: string;
	    syncedAt: number;
	
	    static createFrom(source: any = {}) {
	        return new FileDisplayInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileName = source["fileName"];
	        this.path = source["path"];
	        this.size = source["size"];
	        this.status = source["status"];
	        this.syncedAt = source["syncedAt"];
	    }
	}

}

export namespace search {
	
	export class LocalSearchResult {
	    filePath: string;
	    fileName: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalSearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.fileName = source["fileName"];
	    }
	}

}

