// Light or dark, before anything is drawn (so the page never flashes the wrong colors): "auto" follows the phone or
// computer, "light" and "dark" stay put. The choice is kept on this device.
"use strict";
(function () {
  var media = window.matchMedia ? matchMedia("(prefers-color-scheme: dark)") : null;
  function chosen() { try { return localStorage.getItem("cove.theme") || "auto"; } catch (e) { return "auto"; } }
  function apply() {
    var t = chosen(), dark = t === "dark" || (t === "auto" && !!media && media.matches);
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.style.colorScheme = dark ? "dark" : "light";
    var meta = document.querySelector('meta[name="theme-color"]');
    if (!meta) { meta = document.createElement("meta"); meta.name = "theme-color"; document.head.appendChild(meta); }
    meta.content = dark ? "#0f1218" : "#f5f6f8";
  }
  window.coveTheme = {
    get: chosen,
    set: function (t) { try { localStorage.setItem("cove.theme", t); } catch (e) {} apply(); },
    isDark: function () { return document.documentElement.classList.contains("dark"); },
  };
  if (media && media.addEventListener) media.addEventListener("change", apply);
  apply();
})();
