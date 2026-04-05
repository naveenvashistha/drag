const RecursiveModal = ({ onConfirm, onCancel }) => {
    return (
        <div className="modal-overlay">
            <div className="modal-content">
                <h4>Apply Recursively?</h4>
                <p>Would you like to apply this privacy setting to all inner folders and files?</p>
                <div className="modal-buttons">
                    <button onClick={() => onConfirm(true)}>Yes, all contents</button>
                    <button onClick={() => onConfirm(false)}>No, only this folder</button>
                    <button className="cancel" onClick={onCancel}>Cancel</button>
                </div>
            </div>
        </div>
    );
};

export default RecursiveModal;