document.addEventListener("DOMContentLoaded", function() {
    console.log("JavaScript is working!");
});


function executeQuery() {
    const sql = document.getElementById('sql').value;
    fetch('/query', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json'
        },
        body: JSON.stringify({ sql: sql })
    })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                showError(data.error);
            } else {
                showResults(data);
            }
        })
        .catch(err => showError(err.message));
}

function downloadCSV() {
    const sql = document.getElementById('sql').value;
    window.open(`/download?sql=${encodeURIComponent(sql)}`, '_blank');
}

function showResults(data) {
    const resultDiv = document.getElementById('result');
    let html = `<table>
                <thead><tr>${
        data.columns.map(c => `<th>${c}</th>`).join('')
    }</tr></thead>
                <tbody>`;

    data.data.forEach(row => {
        html += '<tr>';
        data.columns.forEach(col => {
            html += `<td>${row[col] ?? ''}</td>`;
        });
        html += '</tr>';
    });

    html += '</tbody></table>';
    resultDiv.innerHTML = html;
}

function showError(message) {
    const resultDiv = document.getElementById('result');
    resultDiv.innerHTML = `<div class="error">${message}</div>`;
}