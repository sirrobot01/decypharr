package repair

//func (r *Repair) clean(job *Job) error {
//	// Create a new error group
//	g, ctx := errgroup.WithContext(context.Background())
//
//	uniqueItems := make(map[string]string)
//	mu := sync.Mutex{}
//
//	// Limit concurrent goroutines
//	g.SetLimit(10)
//
//	for _, a := range job.Arrs {
//		a := a // Capture range variable
//		g.Go(func() error {
//			// Check if context was canceled
//			select {
//			case <-ctx.Done():
//				return ctx.Err()
//			default:
//			}
//
//			items, err := r.cleanArr(job, a, "")
//			if err != nil {
//				r.logger.Error().Err(err).Msgf("Error cleaning %s", a)
//				return err
//			}
//
//			// Safely append the found items to the shared slice
//			if len(items) > 0 {
//				mu.Lock()
//				for k, v := range items {
//					uniqueItems[k] = v
//				}
//				mu.Unlock()
//			}
//
//			return nil
//		})
//	}
//
//	if err := g.Wait(); err != nil {
//		return err
//	}
//
//	if len(uniqueItems) == 0 {
//		job.CompletedAt = time.Now()
//		job.Status = JobCompleted
//
//		go func() {
//			if err := request.SendDiscordMessage("repair_clean_complete", "success", job.discordContext()); err != nil {
//				r.logger.Error().Msgf("Error sending discord message: %v", err)
//			}
//		}()
//
//		return nil
//	}
//
//	cache := r.deb.Caches["realdebrid"]
//	if cache == nil {
//		return fmt.Errorf("cache not found")
//	}
//	torrents := cache.GetTorrents()
//
//	dangling := make([]string, 0)
//	for _, t := range torrents {
//		if _, ok := uniqueItems[t.Name]; !ok {
//			dangling = append(dangling, t.Id)
//		}
//	}
//
//	r.logger.Info().Msgf("Found %d delapitated items", len(dangling))
//
//	if len(dangling) == 0 {
//		job.CompletedAt = time.Now()
//		job.Status = JobCompleted
//		return nil
//	}
//
//	client := r.deb.Clients["realdebrid"]
//	if client == nil {
//		return fmt.Errorf("client not found")
//	}
//	for _, id := range dangling {
//		err := client.DeleteTorrent(id)
//		if err != nil {
//			return err
//		}
//	}
//
//	return nil
//}
//
//func (r *Repair) cleanArr(j *Job, _arr string, tmdbId string) (map[string]string, error) {
//	uniqueItems := make(map[string]string)
//	a := r.arrs.Get(_arr)
//
//	r.logger.Info().Msgf("Starting repair for %s", a.Name)
//	media, err := a.GetMedia(tmdbId)
//	if err != nil {
//		r.logger.Info().Msgf("Failed to get %s media: %v", a.Name, err)
//		return uniqueItems, err
//	}
//
//	// Create a new error group
//	g, ctx := errgroup.WithContext(context.Background())
//
//	mu := sync.Mutex{}
//
//	// Limit concurrent goroutines
//	g.SetLimit(runtime.NumCPU() * 4)
//
//	for _, m := range media {
//		m := m // Create a new variable scoped to the loop iteration
//		g.Go(func() error {
//			// Check if context was canceled
//			select {
//			case <-ctx.Done():
//				return ctx.Err()
//			default:
//			}
//
//			u := r.getUniquePaths(m)
//			for k, v := range u {
//				mu.Lock()
//				uniqueItems[k] = v
//				mu.Unlock()
//			}
//			return nil
//		})
//	}
//
//	if err := g.Wait(); err != nil {
//		return uniqueItems, err
//	}
//
//	r.logger.Info().Msgf("Repair completed for %s. %d unique items", a.Name, len(uniqueItems))
//	return uniqueItems, nil
//}

//func (r *Repair) getUniquePaths(media arr.Content) map[string]string {
//	// Use zurg setup to check file availability with zurg
//	// This reduces bandwidth usage significantly
//
//	uniqueParents := make(map[string]string)
//	files := media.Files
//	for _, file := range files {
//		target := getSymlinkTarget(file.Path)
//		if target != "" {
//			file.IsSymlink = true
//			dir, f := filepath.Split(target)
//			parent := filepath.Base(filepath.Clean(dir))
//			// Set target path folder/file.mkv
//			file.TargetPath = f
//			uniqueParents[parent] = target
//		}
//	}
//	return uniqueParents
//}
