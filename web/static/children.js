// Search, selection and live counter for the children list.
// The page works without this script: it only adds convenience.
(function () {
  "use strict";

  // List selector: show the chosen list right away. The "Show" button stays
  // in the page for browsers without JavaScript; it is hidden here.
  var listSelect = document.getElementById("list-select");
  var listShow = document.getElementById("list-show");
  if (listSelect && listSelect.form) {
    listSelect.addEventListener("change", function () {
      listSelect.form.submit();
    });
    if (listShow) {
      listShow.hidden = true;
    }
  }

  var toolbar = document.getElementById("toolbar");
  var counter = document.getElementById("counter");
  var table = document.getElementById("children-table");
  var search = document.getElementById("search");
  var noResult = document.getElementById("no-result");
  if (!toolbar || !counter || !table || !search || !noResult) {
    return;
  }

  var rows = Array.prototype.slice.call(table.querySelectorAll("tbody tr[data-search]"));
  var template = counter.getAttribute("data-template") || "";

  // Matches textnorm.Key on the server for accents, case and spaces (enough
  // for French names), but not for every Unicode case.
  function normalize(s) {
    return s
      .normalize("NFD")
      .replace(/[̀-ͯ]/g, "")
      .toLowerCase()
      .split(/\s+/)
      .filter(Boolean)
      .join(" ");
  }

  function checkbox(row) {
    // Not the tolerance checkbox of the family's first row.
    return row.querySelector('input[type="checkbox"]:not(.tolerance)');
  }

  function updateCounter() {
    var children = 0;
    var emails = new Set();
    rows.forEach(function (row) {
      if (checkbox(row).checked) {
        children++;
        emails.add(row.getAttribute("data-email"));
      }
    });
    counter.textContent = template
      .replace("{children}", String(children))
      .replace("{emails}", String(emails.size));
  }

  function applyFilter() {
    var query = normalize(search.value);
    var visible = 0;
    rows.forEach(function (row) {
      var match = row.getAttribute("data-search").indexOf(query) !== -1;
      row.hidden = !match;
      if (match) {
        visible++;
      }
    });
    noResult.hidden = visible !== 0;
  }

  function setVisible(checked) {
    rows.forEach(function (row) {
      var box = checkbox(row);
      if (!row.hidden && !box.disabled) {
        box.checked = checked;
      }
    });
    updateCounter();
  }

  search.addEventListener("input", applyFilter);
  // Enter in the search box must not submit the form.
  search.addEventListener("keydown", function (e) {
    if (e.key === "Enter") {
      e.preventDefault();
    }
  });
  document.getElementById("select-all").addEventListener("click", function () {
    setVisible(true);
  });
  document.getElementById("select-none").addEventListener("click", function () {
    setVisible(false);
  });
  table.addEventListener("change", updateCounter);

  // "Tolérance" flags (SPEC §4.2): one checkbox per family, saved at once.
  // Without this script the form's own save button does the same.
  var tolForm = document.getElementById("tolerance-form");
  if (tolForm) {
    var tolBoxes = Array.prototype.slice.call(table.querySelectorAll("input.tolerance"));
    var tolError = document.getElementById("tolerance-error");
    var tolSave = document.getElementById("tolerance-save");
    var tolTh = document.getElementById("tolerance-th");
    var tolTitle = document.getElementById("tolerance-title");
    var sortButton = document.getElementById("sort-tolerance");
    var tbody = noResult.parentNode;

    var tolState = function () {
      return tolBoxes.filter(function (b) { return b.checked; }).map(function (b) { return b.value; });
    };
    var saved = tolState(); // last state the server confirmed
    var inFlight = false;
    var dirty = false;

    var paint = function (box) {
      rows.forEach(function (row) {
        if (row.getAttribute("data-family") === box.value) {
          row.classList.toggle("row-tolerance", box.checked);
        }
      });
    };
    var revert = function () {
      tolBoxes.forEach(function (box) {
        box.checked = saved.indexOf(box.value) !== -1;
        paint(box);
      });
    };
    var fail = function () {
      dirty = false;
      inFlight = false;
      revert();
      tolError.textContent = tolError.getAttribute("data-message") || "";
      tolError.hidden = false;
    };
    // One request at a time; a change made meanwhile is sent right after
    // with the latest full state, so the last write wins.
    var send = function () {
      inFlight = true;
      dirty = false;
      var sent = tolState();
      fetch(tolForm.action, {
        method: "POST",
        headers: { "HX-Request": "true" },
        body: new URLSearchParams(new FormData(tolForm))
      }).then(function (res) {
        if (!res.ok) {
          fail();
          return;
        }
        saved = sent;
        tolError.hidden = true;
        inFlight = false;
        if (dirty) {
          send();
        }
      }, fail);
    };

    tolBoxes.forEach(function (box) {
      box.addEventListener("change", function () {
        paint(box);
        if (inFlight) {
          dirty = true;
        } else {
          send();
        }
      });
    });
    tolSave.hidden = true;

    // Sort by tolerance: flagged families first, each group in the usual
    // order; whole families move. Not saved, and later changes of a flag
    // do not re-sort.
    var order = rows.slice();
    var groups = [];
    // No prototype: family numbers come from the file and could be
    // "constructor" or "__proto__".
    var byFamily = Object.create(null);
    order.forEach(function (row) {
      var f = row.getAttribute("data-family");
      if (!byFamily[f]) {
        byFamily[f] = [];
        groups.push(byFamily[f]);
      }
      byFamily[f].push(row);
    });
    var flaggedFirst = false;
    sortButton.addEventListener("click", function () {
      flaggedFirst = !flaggedFirst;
      var sequence = order;
      if (flaggedFirst) {
        var isOn = function (g) { return g[0].classList.contains("row-tolerance"); };
        sequence = [].concat.apply([], groups.filter(isOn).concat(groups.filter(function (g) { return !isOn(g); })));
      }
      sequence.forEach(function (row) {
        tbody.insertBefore(row, noResult);
      });
      tolTh.setAttribute("aria-sort", flaggedFirst ? "descending" : "none");
    });
    tolTitle.hidden = true;
    sortButton.hidden = false;
  }

  toolbar.hidden = false;
  counter.hidden = false;
  applyFilter();
  updateCounter();
})();
