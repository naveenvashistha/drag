// import {useState} from 'react';
// import logo from './assets/images/logo-universal.png';
// import './App.css';
// import {Greet} from "../wailsjs/go/main/App";

// function App() {
//     const [resultText, setResultText] = useState("Please enter your name below 👇");
//     const [name, setName] = useState('');
//     const updateName = (e) => setName(e.target.value);
//     const updateResultText = (result) => setResultText(result);

//     function greet() {
//         Greet(name).then(updateResultText);
//     }

//     return (
//         <div id="App">
//             <img src={logo} id="logo" alt="logo"/>
//             <div id="result" className="result">{resultText}</div>
//             <div id="input" className="input-box">
//                 <input id="name" className="input" onChange={updateName} autoComplete="off" name="input" type="text"/>
//                 <button className="btn" onClick={greet}>Greet</button>
//             </div>
//         </div>
//     )
// }

// export default App

import { useState } from "react";
import "./App.css";
import SemanticSearch from "./components/SemanticSearch";
import P2PSearch from "./components/P2PSearch";

// ── Icons (inline SVGs, no extra deps) ──────────────────────────────────────

function IconSearch({ size = 14 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="7" cy="7" r="4.5" />
      <line x1="10.5" y1="10.5" x2="14" y2="14" />
    </svg>
  );
}

function IconNetwork({ size = 14 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="8"  cy="3"  r="2" />
      <circle cx="3"  cy="13" r="2" />
      <circle cx="13" cy="13" r="2" />
      <line x1="8" y1="5" x2="3"  y2="11" />
      <line x1="8" y1="5" x2="13" y2="11" />
      <line x1="5"  y1="13" x2="11" y2="13" />
    </svg>
  );
}

// ── Sidebar Nav Item ─────────────────────────────────────────────────────────

function NavItem({ icon, label, active, onClick }) {
  return (
    <div className={`sidebar-nav-item ${active ? "active" : ""}`} onClick={onClick}>
      <span className="sidebar-nav-icon">{icon}</span>
      {label}
    </div>
  );
}

// ── App Root ─────────────────────────────────────────────────────────────────

export default function App() {
  const [activeTab, setActiveTab] = useState("semantic");

  return (
    <div className="app-shell">
      {/* Sidebar */}
      <aside className="sidebar">
        <div className="sidebar-brand">
          <div className="brand-icon">d</div>
          <span className="brand-name">drag</span>
        </div>

        <div className="sidebar-section">
          <div className="sidebar-section-label">Search</div>
          <NavItem
            icon={<IconSearch />}
            label="Semantic Search"
            active={activeTab === "semantic"}
            onClick={() => setActiveTab("semantic")}
          />
          <NavItem
            icon={<IconNetwork />}
            label="P2P Search"
            active={activeTab === "p2p"}
            onClick={() => setActiveTab("p2p")}
          />
        </div>
      </aside>

      {/* Main panel */}
      <main className="main-content">
        {activeTab === "semantic" && <SemanticSearch />}
        {activeTab === "p2p"      && <P2PSearch />}
      </main>
    </div>
  );
}
