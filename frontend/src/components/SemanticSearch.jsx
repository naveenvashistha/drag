import { useState, useCallback, useRef } from "react";
import {
  GetDirectoryState,
  SetFolderWatchStatus,
  SetFolderVisibility,
  GetFileInfo,
  Search
} from "../../wailsjs/go/main/App";

// ── Inline SVG icons ─────────────────────────────────────────────────────────

function IconSearch({ size = 14 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="7" cy="7" r="4.5" /><line x1="10.5" y1="10.5" x2="14" y2="14" />
    </svg>
  );
}

function IconX({ size = 12 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
      <line x1="2" y1="2" x2="10" y2="10" /><line x1="10" y1="2" x2="2" y2="10" />
    </svg>
  );
}

function IconFolder({ size = 22 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2V7z" />
    </svg>
  );
}

function IconFile({ size = 20 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
      <polyline points="14 2 14 8 20 8" />
    </svg>
  );
}

function IconFileSmall({ size = 14 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
      <polyline points="14 2 14 8 20 8" />
    </svg>
  );
}

// ── Spinner ──────────────────────────────────────────────────────────────────

function Spinner({ size = 14, color = "var(--accent)" }) {
  return (
    <svg
      width={size} height={size} viewBox="0 0 14 14"
      fill="none" stroke={color} strokeWidth="1.8"
      strokeLinecap="round"
      style={{ animation: "spin 0.7s linear infinite", flexShrink: 0 }}
    >
      <circle cx="7" cy="7" r="5" strokeOpacity="0.2" />
      <path d="M7 2a5 5 0 015 5" />
    </svg>
  );
}

// ── Toggle Switch ────────────────────────────────────────────────────────────

function Toggle({ checked, onChange, color = "accent", loading = false, disabled = false }) {
  return (
    <label
      className={`toggle-switch ${color === "green" ? "green" : ""} ${loading || disabled ? "toggle-disabled" : ""}`}
      style={{ pointerEvents: loading || disabled ? "none" : "auto" }}
    >
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => !loading && !disabled && onChange(e.target.checked)}
        disabled={loading || disabled}
      />
      <div className="toggle-track" style={{ opacity: loading ? 0.5 : 1 }} />
      <div className="toggle-thumb" style={{ opacity: loading ? 0 : 1 }} />
      {loading && (
        <div className="toggle-spinner">
          <Spinner
            size={10}
            color={color === "green" ? "var(--green)" : "var(--accent)"}
          />
        </div>
      )}
    </label>
  );
}

// ── Privacy Modal ────────────────────────────────────────────────────────────

function PrivacyModal({ folderName, targetIsPublic, onConfirm, onCancel }) {
  const [recursive, setRecursive] = useState(false);
  const action = targetIsPublic ? "Public" : "Private";

  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div className="modal-box" onClick={(e) => e.stopPropagation()}>
        <div className="modal-title">Apply Privacy Setting{recursive ? " Recursively" : ""}?</div>
        <div className="modal-subtitle">
          Make <span>"{folderName}"</span> and all its subfolders and files <span>{action}</span>?
        </div>

        <label className="modal-checkbox-row">
          <input
            type="checkbox"
            className="modal-checkbox"
            checked={recursive}
            onChange={(e) => setRecursive(e.target.checked)}
          />
          <span className="modal-checkbox-label">Apply recursively to all contents?</span>
        </label>

        <div className="modal-actions">
          <button className="btn btn-ghost" onClick={onCancel}>Cancel</button>
          <button className="btn btn-primary" onClick={() => onConfirm(recursive)}>
            Apply &amp; {action}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── Loading dots ─────────────────────────────────────────────────────────────

function Loading() {
  return (
    <div className="loading-dots">
      <div className="loading-dot" />
      <div className="loading-dot" />
      <div className="loading-dot" />
    </div>
  );
}

// ── Format helpers ────────────────────────────────────────────────────────────

function formatBytes(bytes) {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`;
}

function formatDate(unixSecs) {
  if (!unixSecs) return "—";
  return new Date(unixSecs * 1000).toLocaleString(undefined, {
    year: "numeric", month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit",
  });
}

// ── Directory Card ────────────────────────────────────────────────────────────

function DirectoryCard({ path, state, onWatchToggle, onVisibilityToggle, watchLoading, visibilityLoading }) {
  const folderName = path.split(/[\\/]/).pop() || path;

  return (
    <div className="dir-card">
      <div className="dir-card-header">
        <div className="dir-icon-wrap">
          <IconFolder size={22} />
        </div>
        <div className="dir-name">Folder "{folderName}"</div>

        <div className="dir-toggles">
          {/* Watch Folder */}
          <div className="toggle-row">
            <span className={`toggle-label ${watchLoading ? "" : state.isWatched ? "on" : "off"}`}>
              {watchLoading ? (
                <span style={{ color: "var(--text-muted)", display: "flex", alignItems: "center", gap: 6 }}>
                  <Spinner size={11} color="var(--accent)" /> Updating…
                </span>
              ) : (
                <>Watch Folder · {state.isWatched ? "ON" : "OFF"}</>
              )}
            </span>
            <Toggle
              checked={state.isWatched}
              onChange={onWatchToggle}
              color="accent"
              loading={watchLoading}
            />
          </div>

          {/* Public Access */}
          <div className="toggle-row">
            <span className={`toggle-label ${visibilityLoading ? "" : state.isPublic ? "on" : "off"}`}>
              {visibilityLoading ? (
                <span style={{ color: "var(--text-muted)", display: "flex", alignItems: "center", gap: 6 }}>
                  <Spinner size={11} color="var(--green)" /> Updating…
                </span>
              ) : (
                <>Public Access · {state.isPublic ? "ON" : "OFF"}</>
              )}
            </span>
            <Toggle
              checked={state.isPublic}
              onChange={onVisibilityToggle}
              color="green"
              loading={visibilityLoading}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

// ── File Info Card ────────────────────────────────────────────────────────────

function FileInfoCard({ info }) {
  const statusClass = info.status?.toLowerCase() || "ignored";
  return (
    <div className="file-card">
      <div className="file-card-header">
        <div className="file-icon-wrap">
          <IconFile size={20} />
        </div>
        <div className="file-name-text">{info.fileName}</div>
        <div className={`status-badge ${statusClass}`}>
          <div className="status-dot" />
          {info.status}
        </div>
      </div>

      <div className="file-props-grid">
        <div className="file-prop">
          <span className="prop-label">Path</span>
          <span className="prop-value">{info.path}</span>
        </div>
        <div className="file-prop">
          <span className="prop-label">Size</span>
          <span className="prop-value">{formatBytes(info.size)}</span>
        </div>
        <div className="file-prop">
          <span className="prop-label">Status</span>
          <span className={`prop-value status-${statusClass}`}>{info.status}</span>
        </div>
        <div className="file-prop">
          <span className="prop-label">Last Synced</span>
          <span className="prop-value">{formatDate(info.syncedAt)}</span>
        </div>
      </div>
    </div>
  );
}

// ── Query Results ─────────────────────────────────────────────────────────────

// NOTE: When you wire in a real semantic query backend, replace the mock
// results here with actual Wails calls. The UI structure stays the same.
function ResultsList({ results }) {
  if (!results.length) {
    return (
      <div className="state-empty">
        <IconFileSmall size={40} />
        <span className="state-empty-text">No results found</span>
        <span className="state-empty-sub">Try a different query or index more folders</span>
      </div>
    );
  }

  return (
    <div className="results-list">
      {results.map((r, i) => (
        <div className="result-item" key={r.path}>
          <span className="result-rank">{i + 1}</span>
          <span className="result-file-icon">
            <IconFileSmall size={14} />
          </span>
          <div className="result-info">
            <div className="result-filename">{r.fileName}</div>
            <div className="result-path">{r.filePath}</div>
          </div>
        </div>
      ))}
    </div>
  );
}

// ── Main Component ────────────────────────────────────────────────────────────

export default function SemanticSearch() {
  // ── path search state
  const [pathInput, setPathInput]     = useState("");
  const [pathLoading, setPathLoading] = useState(false);
  const [pathError, setPathError]     = useState(null);
  const [pathType, setPathType]       = useState(null); // "dir" | "file" | null
  const [dirState, setDirState]       = useState(null);
  const [fileInfo, setFileInfo]       = useState(null);

  // ── per-toggle loading states
  const [watchLoading, setWatchLoading]           = useState(false);
  const [visibilityLoading, setVisibilityLoading] = useState(false);
  
  // ── modal state
  const [pendingVisibility, setPendingVisibility] = useState(null); // { targetIsPublic }

  // ── query state
  const [queryInput, setQueryInput]   = useState("");
  const [queryLoading, setQueryLoading] = useState(false);
  const [queryError, setQueryError]   = useState(null);
  const [results, setResults]         = useState(null);

  const debounceTimer = useRef(null);

  // ── Resolve path input ──────────────────────────────────────────────────────

  const resolvePath = useCallback(async (raw) => {
    const p = raw.trim();
    if (!p) {
      setPathType(null); setDirState(null); setFileInfo(null); setPathError(null);
      return;
    }

    setPathLoading(true);
    setPathError(null);
    setDirState(null);
    setFileInfo(null);
    setPathType(null);

    try {
      // Try directory first
      const ds = await GetDirectoryState(p);
      if (ds.IsValid) {
        setPathType("dir");
        setDirState({
          isWatched: ds.IsWatched,
          isPublic:  ds.IsPublic,
          isIgnored: ds.IsIgnored,
        });
        return;
      }

      // Try file
      const fi = await GetFileInfo(p);
      if (fi && fi.fileName) {
        setPathType("file");
        setFileInfo(fi);
        return;
      }

      setPathError("Path not found or not indexed. Make sure the path exists and the folder is being watched.");
    } catch (err) {
      // GetFileInfo throws when not found; treat as not found
      setPathError("Path not found or not indexed.");
    } finally {
      setPathLoading(false);
    }
  }, []);

  const handlePathChange = (val) => {
    setPathInput(val);
    clearTimeout(debounceTimer.current);
    debounceTimer.current = setTimeout(() => resolvePath(val), 600);
  };

  // ── Watch toggle ────────────────────────────────────────────────────────────

  const handleWatchToggle = async (checked) => {
    setWatchLoading(true);
    try {
      await SetFolderWatchStatus(pathInput.trim(), checked);
      setDirState((s) => ({ ...s, isWatched: checked }));
    } catch (err) {
      setPathError(`Failed to update watch status: ${err}`);
    } finally {
      setWatchLoading(false);
    }
  };

  // ── Visibility toggle — opens modal ─────────────────────────────────────────

  const handleVisibilityToggle = (checked) => {
    setPendingVisibility({ targetIsPublic: checked });
  };

  const handleVisibilityConfirm = async (recursive) => {
    const { targetIsPublic } = pendingVisibility;
    setPendingVisibility(null);
    setVisibilityLoading(true);
    try {
      await SetFolderVisibility(pathInput.trim(), targetIsPublic, recursive);
      setDirState((s) => ({ ...s, isPublic: targetIsPublic }));
    } catch (err) {
      setPathError(`Failed to update visibility: ${err}`);
    } finally {
      setVisibilityLoading(false);
    }
  };

  const handleVisibilityCancel = () => {
    setPendingVisibility(null);
  };

  // ── Semantic query ──────────────────────────────────────────────────────────

  const runQuery = useCallback(async (q) => {
    const query = q.trim();
    if (!query) { setResults(null); return; }

    setQueryLoading(true);
    setQueryError(null);

    try {
      const res = await Search(query, true);
      // res is []LocalSearchResult — each item has filePath and fileName
      setResults(res || []);
    } catch (err) {
      setQueryError(`Query failed: ${err}`);
    } finally {
      setQueryLoading(false);
    }
  }, []);

  const handleQueryChange = (val) => {
    setQueryInput(val);
    clearTimeout(debounceTimer.current);
    debounceTimer.current = setTimeout(() => runQuery(val), 500);
  };

  // ── Helpers ──────────────────────────────────────────────────────────────────

  const folderName = pathInput.trim().split(/[\\/]/).pop() || pathInput.trim();

  // ── Render ────────────────────────────────────────────────────────────────────

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">Semantic Search</h1>
      </div>

      <div className="page-body">

        {/* ── Section 1: Path input ── */}
        <div className="search-bar-wrap">
          <span className="search-icon"><IconSearch size={14} /></span>
          <input
            className="search-bar"
            type="text"
            placeholder='Search local folders or files (e.g., C:\Docs\Projects)'
            value={pathInput}
            onChange={(e) => handlePathChange(e.target.value)}
            spellCheck={false}
            autoComplete="off"
          />
          {pathInput && (
            <button className="search-clear" onClick={() => {
              setPathInput(""); setPathType(null);
              setDirState(null); setFileInfo(null); setPathError(null);
            }}>
              <IconX />
            </button>
          )}
        </div>

        {/* Path breadcrumb */}
        {pathInput.trim() && (
          <div className="path-crumb">{pathInput.trim()}</div>
        )}

        {/* Path loading */}
        {pathLoading && <Loading />}

        {/* Path error */}
        {pathError && !pathLoading && (
          <div className="error-msg">{pathError}</div>
        )}

        {/* Directory result */}
        {pathType === "dir" && dirState && !pathLoading && (
          <DirectoryCard
            path={pathInput.trim()}
            state={dirState}
            onWatchToggle={handleWatchToggle}
            onVisibilityToggle={handleVisibilityToggle}
            watchLoading={watchLoading}
            visibilityLoading={visibilityLoading}
          />
        )}

        {/* File result */}
        {pathType === "file" && fileInfo && !pathLoading && (
          <FileInfoCard info={fileInfo} />
        )}

        {/* Empty path state */}
        {!pathInput && !pathLoading && (
          <div className="state-empty">
            <IconFolder size={40} />
            <span className="state-empty-text">Enter a folder or file path above</span>
            <span className="state-empty-sub">You can watch folders, manage visibility, or inspect file metadata</span>
          </div>
        )}

        {/* ── Divider ── */}
        <div className="section-divider">Query</div>

        {/* ── Section 2: Semantic query ── */}
        <div className="search-bar-wrap">
          <span className="search-icon"><IconSearch size={14} /></span>
          <input
            className="search-bar"
            type="text"
            placeholder='Query semantic knowledge (e.g., "AI in art summary")'
            value={queryInput}
            onChange={(e) => handleQueryChange(e.target.value)}
            spellCheck={false}
          />
          {queryInput && (
            <button className="search-clear" onClick={() => {
              setQueryInput(""); setResults(null); setQueryError(null);
            }}>
              <IconX />
            </button>
          )}
        </div>

        {/* Query loading */}
        {queryLoading && <Loading />}

        {/* Query error */}
        {queryError && !queryLoading && (
          <div className="error-msg">{queryError}</div>
        )}

        {/* Query results */}
        {results !== null && !queryLoading && (
          <ResultsList results={results} />
        )}

        {/* Query empty state */}
        {results === null && !queryInput && !queryLoading && (
          <div className="state-empty">
            <IconSearch size={40} />
            <span className="state-empty-text">Enter a query to search your knowledge</span>
            <span className="state-empty-sub">Results are ranked by semantic similarity</span>
          </div>
        )}

      </div>

      {/* Privacy modal */}
      {pendingVisibility && (
        <PrivacyModal
          folderName={folderName}
          targetIsPublic={pendingVisibility.targetIsPublic}
          onConfirm={handleVisibilityConfirm}
          onCancel={handleVisibilityCancel}
        />
      )}
    </>
  );
}
