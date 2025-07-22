### Manually Importing Multi-Season Series Packs into Sonarr with Decypharr

Sonarr is excellent for managing individual episodes or single-season packs, but it generally **doesn't support automatic importing of multi-season or full series packs**. However, you can still successfully import these larger collections into Sonarr by using Decypharr and a manual import process.

__**Step-by-Step Guide**__

1.  **Add the Series to Sonarr:**
    * Add the Series itself to Sonarr, but don't search for it.

2.  **Add the Series Pack to Decypharr:**
    * Find and copy the **Magnet Link** for the series pack.
    * In Decypharr's web UI, navigate to the **Download** tab.
    * Paste the magnet link into the appropriate field.
    * Under "Post Download Action," ensure **"Symlink"** is selected.
    * In the **"Arr (if any)"** box, type `temp`. (You can use anything here, as long as Decypharr has the ability to make a new directory in your Symlinks directory, e.g. `temp`, `placeholder`, or `series_packs`)
    * Click **"Add to Download Queue."**

3.  **Locate the Download in Sonarr:**
    * Assuming the torrent has been cached and processed by Decypharr, you can move to the Sonarr interface.
    * Go to Sonarr's Wanted tab, and click the **"Manual Import"** button.

4.  **Navigate to the Symlink Folder:**
    * An interactive file browser will appear. This is the **most crucial step** for multi-season packs.
      * Navigate to your **symlinks folder** (e.g., `/mnt/symlinks`), importantly not your `__all__` folder, then click into the **`temp`** folder.
      * Inside this folder, you should find the **downloaded series pack folder**.

5.  **Import the Show Manually:**
    * Select the downloaded series pack folder.
    * Follow Sonarr's prompts to manually import the show. Sonarr will then analyze the contents of the folder and allow you to import all seasons and episodes. Make sure to select the “Move files” option before importing. 

By following these steps, you can successfully import multi-season series packs into Sonarr using Decypharr, even though Sonarr doesn't automatically recognize all seasons during the initial download.
